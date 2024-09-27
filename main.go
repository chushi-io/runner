package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/chushi-io/chushi-go-sdk"
	"github.com/chushi-io/timber/adapter"
	"github.com/hashicorp/go-tfe"
	"github.com/hashicorp/terraform-exec/tfexec"
	"go.uber.org/zap"
	"io"
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

func main() {
	directory := flag.String("directory", "", "The working directory")
	//parallelism := flag.Int("parallelism", 10, "Threads to run")
	version := flag.String("version", "latest", "Tofu version to run")
	logAddress := flag.String("log-address", "", "Endpoint for streaming logs")

	flag.Parse()

	operation := os.Args[1]

	_, err := chushi.New(tfe.DefaultConfig())
	if err != nil {
		logger.Fatal("failed to initialize chushi SDK", zap.Error(err))
	}
	ctx := context.Background()

	logger.Info("installing tofu", zap.String("directory", *directory))
	tf, err := ensureTofu(*directory, *version)
	if err != nil {
		logger.Fatal("failed to install tofu", zap.Error(err))
	}

	logAdapter := adapter.New(*logAddress, os.Getenv("TFE_TOKEN"), fmt.Sprintf("%s/%s.log", "", "plan"))

	if err = setup(tf, logAdapter); err != nil {
		logger.Fatal("failed to setup execution", zap.Error(err))
	}

	if err = tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		logger.Fatal("failed to initialize tofu", zap.Error(err))
	}

	switch operation {
	case "plan":
		err = opPlan(ctx, tf)
	case "apply":
	case "destroy":
	}
	// Parse our environment and pass it to the Tofu execution

}

func ensureTofu(workingDirectory string, tofuVersion string) (*tfexec.Terraform, error) {
	if tofuVersion == "latest" || tofuVersion == "" {
		tofuVersion = DEFAULT_TOFU_VERSION
	}

	execPath, err := installBinary(tofuVersion)
	if err != nil {
		return nil, err
	}

	return tfexec.NewTerraform(workingDirectory, execPath)
}

func setup(tf *tfexec.Terraform, output io.Writer) error {
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
	tf.SetStdout(output)
	tf.SetStderr(output)
	return nil
}

func opPlan(ctx context.Context, tf *tfexec.Terraform) error {
	planOnly := flag.Bool("plan-only", false, "Only run a plan operation")
	// This is hacky. We'll want to move to a full list of string slices
	targets := flag.String("targets", "", "Target addresses")
	opts := []tfexec.PlanOption{
		tfexec.Out("tfplan"),
	}
	if *planOnly {
		opts = append(opts, tfexec.Lock(false))
	}
	if *targets != "" {
		targetAddrs := strings.Split(*targets, ",")
		for _, targetAddr := range targetAddrs {
			opts = append(opts, tfexec.Target(targetAddr))
		}
	}
	hasChanges, err := tf.Plan(ctx, opts...)
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
