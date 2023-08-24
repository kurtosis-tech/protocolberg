package finalization_tests

import (
	"context"
	"encoding/json"
	"fmt"
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
	enclaveNamePrefix  = "finalization-test"
	eth2Package        = "github.com/kurtosis-tech/eth2-package"
	inputFile          = "./input_args.json"
	defaultParallelism = 4
	isNotDryRun        = false

	// we use the main.star file at root of the package
	pathToMainFile = ""
	// the main function is called run
	runFunctionName = ""

	beaconServiceHttpPortId   = "http"
	finalizationRetryInterval = time.Second * 10
	// 3 seconds per slot, 160 slots, some buffer
	timeoutForFinalization = 3 * 180 * time.Second
)

var noExperimentalFeatureFlags = []kurtosis_core_rpc_api_bindings.KurtosisFeatureFlag{}

type finalization struct {
	Data data `json:"data"`
}

type data struct {
	Finalized finalized `json:"finalized"`
}

type finalized struct {
	Epoch string `json:"epoch"`
}

func TestEth2Package_DenebCapellaFinalization(t *testing.T) {
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
	logrus.Info("Executing the Starlark Package")
	packageRunResult, err := enclaveCtx.RunStarlarkRemotePackageBlocking(ctx, eth2Package, pathToMainFile, runFunctionName, inputParametersAsJSONString, isNotDryRun, defaultParallelism, noExperimentalFeatureFlags)
	require.NoError(t, err, "An unexpected error occurred while executing the package")
	require.Nil(t, packageRunResult.InterpretationError)
	require.Empty(t, packageRunResult.ValidationErrors)
	require.Nil(t, packageRunResult.ExecutionError)

	var beaconNodes []*services.ServiceContext
	enclaveServices, err := enclaveCtx.GetServices()
	require.Nil(t, err)
	for serviceName := range enclaveServices {
		serviceNameStr := string(serviceName)
		if strings.HasPrefix(serviceNameStr, "cl-") && !strings.HasSuffix(serviceNameStr, "-validator") {
			logrus.Info("Found beacon node with name '%v'", serviceNameStr)
			beaconService, err := enclaveCtx.GetServiceContext(serviceNameStr)
			require.NoError(t, err)
			beaconNodes = append(beaconNodes, beaconService)
		}
	}

	wg := sync.WaitGroup{}
	for _, beaconNodeServiceCtx := range beaconNodes {
		go func(beaconNodeServiceCtx *services.ServiceContext) {
			for {
				wg.Add(1)
				privatePorts := beaconNodeServiceCtx.GetPrivatePorts()
				httpPort, found := privatePorts[beaconServiceHttpPortId]
				require.True(t, found)
				epochsFinalized := getFinalization(t, httpPort.GetNumber())
				logrus.Infof("Queried service '%s' got finalized epoch '%d'", beaconNodeServiceCtx.GetServiceName(), epochsFinalized)
				if epochsFinalized > 0 {
					wg.Done()
				}
				logrus.Infof("Pausing querying service '%s' for '%d' seconds", beaconNodeServiceCtx.GetServiceName(), finalizationRetryInterval)
				time.Sleep(finalizationRetryInterval)
			}
		}(beaconNodeServiceCtx)
	}

	didWaitTimeout := waitTimeout(wg, timeoutForFinalization)
	require.False(t, didWaitTimeout)
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
