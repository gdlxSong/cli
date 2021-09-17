// ------------------------------------------------------------
// Copyright (c) Microsoft Corporation and Dapr Contributors.
// Licensed under the MIT License.
// ------------------------------------------------------------

package standalone

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/Pallinder/sillyname-go"
	"github.com/dapr/dapr/pkg/components"
	modes "github.com/dapr/dapr/pkg/config/modes"
	"github.com/phayes/freeport"
	"gopkg.in/yaml.v2"
)

const sentryDefaultAddress = "localhost:50001"

// RunConfig represents the application configuration parameters.
type RunConfig struct {
	AppID              string   //dapr-cli启动指定的APPID
	AppPort            int      //即用户服务启动的端口
	HTTPPort           int      //边车代理的HTTP端口
	GRPCPort           int      //编程代理的GRPC端口
	ConfigFile         string   //边车的配置文件：$HOME/.dapr/config.yaml
	Protocol           string   //？
	Arguments          []string //用户服务的cobra的参数
	EnableProfiling    bool     //?
	ProfilePort        int      //?
	LogLevel           string   //边车日志级别
	MaxConcurrency     int      //dapr-cli所起的服务的并发级别参数
	PlacementHostAddr  string   //?
	ComponentsPath     string   //dapr-cli启动服务的时候指定的components文件夹路径
	AppSSL             bool     //是否启动https
	MetricsPort        int      //metrics port
	MaxRequestBodySize int      //http-request-body-size.
}

// RunOutput represents the run output.
type RunOutput struct {
	DaprCMD      *exec.Cmd
	DaprHTTPPort int
	DaprGRPCPort int
	AppID        string
	AppCMD       *exec.Cmd
}

func getDaprCommand(appID string, daprHTTPPort int, daprGRPCPort int, appPort int, configFile, protocol string, enableProfiling bool, profilePort int, logLevel string, maxConcurrency int, placementHostAddr string, componentsPath string, appSSL bool, metricsPort int, requestBodySize int) (*exec.Cmd, int, int, int, error) {
	if daprHTTPPort < 0 {
		port, err := freeport.GetFreePort()
		if err != nil {
			return nil, -1, -1, -1, err
		}

		daprHTTPPort = port
	}

	if daprGRPCPort < 0 {
		grpcPort, err := freeport.GetFreePort()
		if err != nil {
			return nil, -1, -1, -1, err
		}

		daprGRPCPort = grpcPort
	}

	if metricsPort < 0 {
		var err error
		metricsPort, err = freeport.GetFreePort()
		if err != nil {
			return nil, -1, -1, -1, err
		}
	}

	if maxConcurrency < 1 {
		maxConcurrency = -1
	}

	if requestBodySize < 0 {
		requestBodySize = -1
	}

	daprCMD := binaryFilePath(defaultDaprBinPath(), "daprd")

	args := []string{
		"--app-id", appID,
		"--dapr-http-port", strconv.Itoa(daprHTTPPort),
		"--dapr-grpc-port", strconv.Itoa(daprGRPCPort),
		"--log-level", logLevel,
		"--app-max-concurrency", strconv.Itoa(maxConcurrency),
		"--app-protocol", protocol,
		"--components-path", componentsPath,
		"--metrics-port", strconv.Itoa(metricsPort),
		"--dapr-http-max-request-size", strconv.Itoa(requestBodySize),
	}
	if appPort > -1 {
		args = append(args, "--app-port", strconv.Itoa(appPort))
	}
	args = append(args, "--placement-host-address")

	// if placementHostAddr does not contain port, add default port value
	if indx := strings.Index(placementHostAddr, ":"); indx == -1 {
		if runtime.GOOS == daprWindowsOS {
			args = append(args, fmt.Sprintf("%s:6050", placementHostAddr))
		} else {
			args = append(args, fmt.Sprintf("%s:50005", placementHostAddr))
		}
	} else {
		args = append(args, placementHostAddr)
	}

	if configFile != "" {
		args = append(args, "--config", configFile)
		sentryAddress := mtlsEndpoint(configFile)
		if sentryAddress != "" {
			// mTLS is enabled locally, set it up
			args = append(args, "--enable-mtls", "--sentry-address", sentryAddress)
		}
	}

	if enableProfiling {
		if profilePort == -1 {
			pp, err := freeport.GetFreePort()
			if err != nil {
				return nil, -1, -1, -1, err
			}
			profilePort = pp
		}

		args = append(
			args,
			"--enable-profiling",
			"--profile-port", strconv.Itoa(profilePort))
	}

	if appSSL {
		args = append(args, "--app-ssl")
	}

	cmd := exec.Command(daprCMD, args...)
	return cmd, daprHTTPPort, daprGRPCPort, metricsPort, nil
}

func mtlsEndpoint(configFile string) string {
	if configFile == "" {
		return ""
	}

	b, err := ioutil.ReadFile(configFile)
	if err != nil {
		return ""
	}

	var config mtlsConfig
	err = yaml.Unmarshal(b, &config)
	if err != nil {
		return ""
	}

	if config.Spec.MTLS.Enabled {
		return sentryDefaultAddress
	}
	return ""
}

func getAppCommand(httpPort, grpcPort, metricsPort int, command string, args []string) (*exec.Cmd, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	cmd.Env = append(
		cmd.Env,
		fmt.Sprintf("DAPR_HTTP_PORT=%v", httpPort),
		fmt.Sprintf("DAPR_GRPC_PORT=%v", grpcPort),
		fmt.Sprintf("DAPR_METRICS_PORT=%v", metricsPort))

	return cmd, nil
}

func Run(config *RunConfig) (*RunOutput, error) {
	appID := config.AppID
	if appID == "" {
		appID = strings.ReplaceAll(sillyname.GenerateStupidName(), " ", "-")
	}

	//检查componnets目录是否存在
	_, err := os.Stat(config.ComponentsPath)
	if err != nil {
		return nil, err
	}

	//通过ps命令来获取进程列表
	dapr, err := List()
	if err != nil {
		return nil, err
	}

	for _, a := range dapr {
		if appID == a.AppID {
			return nil, fmt.Errorf("dapr with ID %s is already running", appID)
		}
	}

	//加载components...
	componentsLoader := components.NewStandaloneComponents(modes.StandaloneConfig{ComponentsPath: config.ComponentsPath})
	_, err = componentsLoader.LoadComponents()
	if err != nil {
		return nil, err
	}

	//通过daprd生成启动命令
	daprCMD, daprHTTPPort, daprGRPCPort, metricsPort, err := getDaprCommand(appID, config.HTTPPort, config.GRPCPort, config.AppPort, config.ConfigFile, config.Protocol, config.EnableProfiling, config.ProfilePort, config.LogLevel, config.MaxConcurrency, config.PlacementHostAddr, config.ComponentsPath, config.AppSSL, config.MetricsPort, config.MaxRequestBodySize)
	if err != nil {
		return nil, err
	}

	for _, a := range dapr {
		if daprHTTPPort == a.HTTPPort {
			return nil, fmt.Errorf("there's already a Dapr instance running with http port %v", daprHTTPPort)
		} else if daprGRPCPort == a.GRPCPort {
			return nil, fmt.Errorf("there's already a Dapr instance running with gRPC port %v", daprGRPCPort)
		}
	}

	argCount := len(config.Arguments)
	runArgs := []string{}
	var appCMD *exec.Cmd

	if argCount > 0 {
		cmd := config.Arguments[0]
		if len(config.Arguments) > 1 {
			runArgs = config.Arguments[1:]
		}

		appCMD, err = getAppCommand(daprHTTPPort, daprGRPCPort, metricsPort, cmd, runArgs)
		if err != nil {
			return nil, err
		}
	}

	return &RunOutput{
		DaprCMD:      daprCMD,
		AppCMD:       appCMD,
		AppID:        appID,
		DaprHTTPPort: daprHTTPPort,
		DaprGRPCPort: daprGRPCPort,
	}, nil
}
