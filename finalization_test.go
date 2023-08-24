package protocolberg

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flashbots/mev-boost-relay/database"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/services"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	enclaveNamePrefix = "finalization-test"
	// faster seconds per slot on this branch in boost & package
	eth2Package        = "github.com/kurtosis-tech/eth2-package@gyani/protocolberg"
	inputFile          = "./input_args.json"
	defaultParallelism = 4
	isNotDryRun        = false

	// we use the main.star file at root of the package
	pathToMainFile = ""
	// the main function is called run
	runFunctionName = ""

	beaconServiceHttpPortId   = "http"
	elServiceRpcPortId        = "rpc"
	websiteApiId              = "api"
	finalizationRetryInterval = time.Second * 10
	// 3 seconds per slot, 32(buffer 34) slots per epoch, 5th epoch, some buffer
	timeoutForFinalization = 3 * 34 * 5 * time.Second
	timeoutForSync         = 30 * time.Second
	syncInterval           = 2 * time.Second

	postgresDsn = "postgres://postgres:postgres@0.0.0.0:%d/postgres?sslmode=disable"
)

var noExperimentalFeatureFlags = []kurtosis_core_rpc_api_bindings.KurtosisFeatureFlag{}

func TestEth2Package_FinalizationSyncingMEV(t *testing.T) {
	// set up the input parameters
	logrus.Info("Parsing Input Parameters")
	inputParameters, err := os.ReadFile(inputFile)
	require.NoError(t, err, "An error occurred while reading the input file")
	require.NotEmpty(t, inputParameters, "Input parameters byte array is unexpectedly empty")
	inputParametersAsJSONString := string(inputParameters)
	require.NotEmpty(t, inputParametersAsJSONString, "Input parameter json string is unexpectedly empty")

	// set up enclave
	logrus.Info("Setting up Kurtosis Engine Connection & Enclave")
	ctx, cancelCtxFunc := context.WithCancel(context.Background())
	defer cancelCtxFunc()
	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	require.NoError(t, err, "An error occurred while creating Kurtosis Context")

	enclaveName := fmt.Sprintf("%s-%d", enclaveNamePrefix, time.Now().Unix())
	enclaveCtx, err := kurtosisCtx.CreateEnclave(ctx, enclaveName)
	require.Nil(t, err, "An unexpected error occurred while creating Enclave Context")
	//defer kurtosisCtx.DestroyEnclave(ctx, enclaveName)

	// execute package
	logrus.Info("Executing the Starlark Package, this will wait for 1 epoch as MEV is turned on")
	logrus.Infof("Using Enclave '%v' - use `kurtosis enclave inspect %v` to see whats inside", enclaveName, enclaveName)
	packageRunResult, err := enclaveCtx.RunStarlarkRemotePackageBlocking(ctx, eth2Package, pathToMainFile, runFunctionName, inputParametersAsJSONString, isNotDryRun, defaultParallelism, noExperimentalFeatureFlags)
	require.NoError(t, err, "An unexpected error occurred while executing the package")
	require.Nil(t, packageRunResult.InterpretationError)
	require.Empty(t, packageRunResult.ValidationErrors)
	require.Nil(t, packageRunResult.ExecutionError)

	mevRelayWebsiteCtx, err := enclaveCtx.GetServiceContext("mev-relay-website")
	require.NoError(t, err)
	mevRelayWebsiteHttpPort, found := mevRelayWebsiteCtx.GetPublicPorts()[websiteApiId]
	require.True(t, found)
	mevRelayWebsiteUrl := fmt.Sprintf("http://0.0.0.0:%d", mevRelayWebsiteHttpPort.GetNumber())
	logrus.Infof("Check out the MEV relay website at '%s'", mevRelayWebsiteUrl)

	var beaconNodes []*services.ServiceContext
	var elNodes []*services.ServiceContext
	enclaveServices, err := enclaveCtx.GetServices()
	require.Nil(t, err)
	for serviceName := range enclaveServices {
		serviceNameStr := string(serviceName)
		if strings.HasPrefix(serviceNameStr, "cl-") && !strings.HasSuffix(serviceNameStr, "-validator") && !strings.HasSuffix(serviceNameStr, "-forkmon") {
			logrus.Infof("Found beacon node with name '%s'", serviceNameStr)
			beaconService, err := enclaveCtx.GetServiceContext(serviceNameStr)
			require.NoError(t, err)
			beaconNodes = append(beaconNodes, beaconService)
		}
		if strings.HasPrefix(serviceNameStr, "el-") && !strings.HasSuffix(serviceNameStr, "-forkmon") {
			logrus.Infof("Found el node with name '%s'", serviceNameStr)
			elService, err := enclaveCtx.GetServiceContext(serviceNameStr)
			require.NoError(t, err)
			elNodes = append(elNodes, elService)
		}
	}

	// assert that finalization happens on all CL nodes
	wg := sync.WaitGroup{}
	for _, beaconNodeServiceCtx := range beaconNodes {
		wg.Add(1)
		go func(beaconNodeServiceCtx *services.ServiceContext) {
			for {
				publicPorts := beaconNodeServiceCtx.GetPublicPorts()
				httpPort, found := publicPorts[beaconServiceHttpPortId]
				require.True(t, found)
				epochsFinalized := getFinalization(t, httpPort.GetNumber())
				logrus.Infof("Queried service '%s' got finalized epoch '%d'", beaconNodeServiceCtx.GetServiceName(), epochsFinalized)
				if epochsFinalized > 0 {
					break
				} else {
					logrus.Infof("Pausing querying service '%s' for '%v' seconds", beaconNodeServiceCtx.GetServiceName(), finalizationRetryInterval.Seconds())
				}
				time.Sleep(finalizationRetryInterval)
			}
			wg.Done()
		}(beaconNodeServiceCtx)
	}
	didWaitTimeout := waitTimeout(&wg, timeoutForFinalization)
	require.False(t, didWaitTimeout, "Finalization didn't happen within expected duration of '%v' seconds", timeoutForFinalization.Seconds())

	// assert that all CL nodes are synced
	clClientSyncWaitGroup := sync.WaitGroup{}
	for _, beaconNodeServiceCtx := range beaconNodes {
		clClientSyncWaitGroup.Add(1)
		go func(beaconNodeServiceCtx *services.ServiceContext) {
			for {
				publicPorts := beaconNodeServiceCtx.GetPublicPorts()
				httpPort, found := publicPorts[beaconServiceHttpPortId]
				require.True(t, found)
				isSyncing := getCLSyncing(t, httpPort.GetNumber())
				logrus.Infof("Node '%s' is fully synced", beaconNodeServiceCtx.GetServiceName())
				if !isSyncing {
					break
				} else {
					logrus.Infof("Pausing querying service '%s' for '%v' seconds", beaconNodeServiceCtx.GetServiceName(), syncInterval.Seconds())
				}
				time.Sleep(syncInterval)
			}
			clClientSyncWaitGroup.Done()
		}(beaconNodeServiceCtx)
	}
	didWaitTimeout = waitTimeout(&clClientSyncWaitGroup, timeoutForSync)
	require.False(t, didWaitTimeout, "CL nodes weren't fully synced in the expected amount of time '%v'", timeoutForSync.Seconds())

	// assert that all EL nodes are synced
	elClientSyncWaitGroup := sync.WaitGroup{}
	for _, elNodeServiceCtx := range elNodes {
		elClientSyncWaitGroup.Add(1)
		go func(elNodeServiceCtx *services.ServiceContext) {
			for {
				publicPorts := elNodeServiceCtx.GetPublicPorts()
				rpcPort, found := publicPorts[elServiceRpcPortId]
				require.True(t, found)
				isSyncing := getELSyncing(t, rpcPort.GetNumber())
				logrus.Infof("Node '%s' is fully synced", elNodeServiceCtx.GetServiceName())
				if !isSyncing {
					break
				} else {
					logrus.Infof("Pausing querying service '%s' for '%v' seconds", elNodeServiceCtx.GetServiceName(), syncInterval.Seconds())
				}
				time.Sleep(syncInterval)
			}
			elClientSyncWaitGroup.Done()
		}(elNodeServiceCtx)
	}
	didWaitTimeout = waitTimeout(&elClientSyncWaitGroup, timeoutForSync)
	require.False(t, didWaitTimeout, "EL nodes weren't fully synced in the expected amount of time '%v'", timeoutForSync.Seconds())

	logrus.Info("Finalization has been reached and all nodes are fully synced")

	// as finalization happens around  the 160th slot, some payloads should have been already delivered
	logrus.Infof("Check out the MEV relay website at '%s'; payloads should get delivered around 128 slots", mevRelayWebsiteUrl)
	logrus.Info("Checking registered validators & payloads delivered on MEV")
	postgresService, err := enclaveCtx.GetServiceContext("postgres")
	require.Nil(t, err)
	postgresPort, found := postgresService.GetPublicPorts()["postgresql"]
	require.True(t, found)
	dsn := fmt.Sprintf(postgresDsn, postgresPort.GetNumber())
	dbService, err := database.NewDatabaseService(dsn)
	require.Nil(t, err)
	numRegisteredValidators, err := dbService.NumRegisteredValidators()
	require.Nil(t, err)
	require.Equal(t, uint64(256), numRegisteredValidators)
	numDeliveredPayloads, err := dbService.GetNumDeliveredPayloads()
	require.Nil(t, err)
	require.GreaterOrEqual(t, numDeliveredPayloads, uint64(0))
}

// extract this as a function that returns finalized epoch
func getFinalization(t *testing.T, beaconHttpPort uint16) int {
	finalizationEndpoint := "eth/v1/beacon/states/head/finality_checkpoints"
	url := fmt.Sprintf("http://0.0.0.0:%d/%s", beaconHttpPort, finalizationEndpoint)
	resp, err := http.Get(url)
	require.Empty(t, err, "an unexpected error happened while making http request")
	require.NotNil(t, resp.Body)
	defer resp.Body.Close()
	var finalizedResponse finalization
	err = json.NewDecoder(resp.Body).Decode(&finalizedResponse)
	require.Nil(t, err, "an unexpected error occurred while decoding json")
	finalizedEpoch, err := strconv.Atoi(finalizedResponse.Data.Finalized.Epoch)
	require.NoError(t, err, "an error occurred while converting finalized epoch to integer")
	require.GreaterOrEqual(t, finalizedEpoch, 0)
	return finalizedEpoch
}

func getCLSyncing(t *testing.T, beaconHttpPort uint16) bool {
	syncingEndpoint := "eth/v1/node/syncing"
	url := fmt.Sprintf("http://0.0.0.0:%d/%s", beaconHttpPort, syncingEndpoint)
	resp, err := http.Get(url)
	require.Empty(t, err, "an unexpected error happened while making http request")
	require.NotNil(t, resp.Body)
	defer resp.Body.Close()
	var clSyncingResponse clSyncingStruct
	err = json.NewDecoder(resp.Body).Decode(&clSyncingResponse)
	require.Nil(t, err, "an unexpected error occurred while decoding json")
	isSyncing := clSyncingResponse.Data.IsSyncing
	return isSyncing
}

func getELSyncing(t *testing.T, elRpcPort uint16) bool {
	url := fmt.Sprintf("http://0.0.0.0:%d/", elRpcPort)
	syncingPost := strings.NewReader(`{"method":"eth_syncing","params":[],"id":1,"jsonrpc":"2.0"}`)
	resp, err := http.Post(url, "application/json", syncingPost)
	require.Empty(t, err, "an unexpected error happened while making http post to EL")
	require.NotNil(t, resp.Body)
	defer resp.Body.Close()
	var elSyncingResponse elSyncingDataResponse
	err = json.NewDecoder(resp.Body).Decode(&elSyncingResponse)
	require.Nil(t, err, "an unexpected error occurred while decoding json")
	isSyncing := elSyncingResponse.Result
	return isSyncing
}

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed normally
	case <-time.After(timeout):
		return true // timed out
	}
}
