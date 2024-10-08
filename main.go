package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/chushi-io/timber/adapter"
	"github.com/opentofu/tofu-exec/tfexec"
	"go.uber.org/zap"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var logger *zap.Logger

var DEFAULT_TOFU_VERSION = "1.8.2"

func init() {
	logger, _ = zap.NewProduction()
}

var (
	planOnly bool
	targets  string
	destroy  bool
)

func main() {
	directory := flag.String("directory", "", "The working directory")
	//parallelism := flag.Int("parallelism", 10, "Threads to run")
	version := flag.String("version", "latest", "Tofu version to run")
	logAddress := flag.String("log-address", "", "Endpoint for streaming logs")
	runId := flag.String("run-id", "", "ID of the current run")
	debug := flag.Bool("debug", false, "Log debug statements")

	// Operation specific flags, these are bound to vars
	flag.BoolVar(&planOnly, "plan-only", false, "Only run a plan operation")
	flag.StringVar(&targets, "targets", "", "Target addresses")
	flag.BoolVar(&destroy, "destroy", false, "Run destroy operation")

	flag.Parse()

	operation := flag.Arg(0)

	if *debug {
		logger, _ = zap.NewDevelopment()
	}

	ctx := context.Background()

	logger.Info("installing tofu", zap.String("directory", *directory))
	tf, err := ensureTofu(*directory, *version)
	if err != nil {
		logger.Fatal("failed to install tofu", zap.Error(err))
	}

	logger.Debug("setting up log adapter")
	logAdapter := adapter.New(*logAddress, os.Getenv("TFE_TOKEN"), fmt.Sprintf("%s_%s.log", *runId, "plan"))

	logger.Info("setting up execution environment")
	if err = setup(tf); err != nil {
		logger.Fatal("failed to setup execution", zap.Error(err))
	}

	logger.Info("initializing tofu")
	if err = tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		fmt.Println("Tofu failed to initialize")
		fmt.Println(err)
		logger.Fatal("failed to initialize tofu", zap.Error(err))
	}
	fmt.Println("Tofu initialized")

	switch operation {
	case "plan":
		err = opPlan(ctx, io.MultiWriter(logAdapter, os.Stdout), tf)
	case "apply":
	}
	// Parse our environment and pass it to the Tofu execution
	if err != nil {
		log.Fatal(err)
	}
}

func ensureTofu(workingDirectory string, tofuVersion string) (*tfexec.Terraform, error) {
	if tofuVersion == "latest" || tofuVersion == "" {
		tofuVersion = DEFAULT_TOFU_VERSION
	}

	logger.Info("found tofu version", zap.String("version", tofuVersion))
	execPath, err := installBinary(tofuVersion)
	if err != nil {
		return nil, err
	}

	return tfexec.NewTerraform(workingDirectory, execPath)
}

func setup(tf *tfexec.Terraform) error {
	envs := map[string]string{}
	for _, envVar := range os.Environ() {
		chunks := strings.Split(envVar, "=")
		if len(chunks) != 2 {
			continue
		}
		envs[chunks[0]] = chunks[1]
	}
	if err := tf.SetEnv(envs); err != nil {
		return err
	}

	if err := tf.SetLogProvider("TRACE"); err != nil {
		fmt.Println(err)
	}
	return nil
}

func opPlan(ctx context.Context, writer io.Writer, tf *tfexec.Terraform) error {

	opts := []tfexec.PlanOption{
		tfexec.Out("tfplan"),
	}
	if planOnly {
		opts = append(opts, tfexec.Lock(false))
	}
	if destroy {
		opts = append(opts, tfexec.Destroy(true))
	}
	if targets != "" {
		targetAddrs := strings.Split(targets, ",")
		for _, targetAddr := range targetAddrs {
			opts = append(opts, tfexec.Target(targetAddr))
		}
	}
	hasChanges, err := tf.PlanJSON(ctx, writer, opts...)
	if err != nil {
		return err
	}

	if hasChanges {
		fmt.Println("Changes occured!")
	}
	// Upload the plan output
	// Convert the JSON plan
	// Update the plan status
	return nil
}

func installBinary(tofuVersion string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	arch := "amd64"
	if runtime.GOARCH == "arm" {
		arch = "arm64"
	}
	binaryUrl := fmt.Sprintf(
		"https://github.com/opentofu/opentofu/releases/download/v%s/tofu_%s_%s_%s.tar.gz",
		tofuVersion,
		tofuVersion,
		runtime.GOOS,
		arch,
	)
	logger.Info("downloading tofu archive", zap.String("url", binaryUrl))
	resp, err := http.Get(binaryUrl)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()
	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()

		switch {

		// if no more files are found return
		case err == io.EOF:
			return "", errors.New("failed to find tofu binary in archive")

		// return any other error
		case err != nil:
			return "", err

		// if the header is nil, just skip it (not sure how this happens)
		case header == nil:
			continue
		}

		if header.Name == "tofu" {
			outfile, err := os.Create("tofu")
			if err != nil {
				return "", err
			}
			defer outfile.Close()
			if _, err := io.Copy(outfile, tr); err != nil {
				return "", err
			}

			if err := os.Chmod("tofu", os.FileMode(header.Mode)); err != nil {
				return "", err
			}
			return filepath.Join(cwd, "tofu"), nil
		}
	}
}
