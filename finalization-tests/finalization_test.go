package finalization_tests

import (
	"context"
	"fmt"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"os"
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
)

var noExperimentalFeatureFlags = []kurtosis_core_rpc_api_bindings.KurtosisFeatureFlag{}

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
	defer kurtosisCtx.DestroyEnclave(ctx, enclaveName)

	// execute package
	logrus.Info("Executing the Starlark Package")
	packageRunResult, err := enclaveCtx.RunStarlarkRemotePackageBlocking(ctx, eth2Package, pathToMainFile, runFunctionName, inputParametersAsJSONString, isNotDryRun, defaultParallelism, noExperimentalFeatureFlags)
	require.NoError(t, err, "An unexpected error occurred while executing the package")
	require.Nil(t, packageRunResult.InterpretationError)
	require.Empty(t, packageRunResult.ValidationErrors)
	require.Nil(t, packageRunResult.ExecutionError)
}
