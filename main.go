package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/chushi-io/chushi-go-sdk"
	install "github.com/chushi-io/hc-install"
	"github.com/chushi-io/hc-install/product"
	"github.com/chushi-io/hc-install/releases"
	"github.com/chushi-io/hc-install/src"
	"github.com/chushi-io/timber/adapter"
	"github.com/hashicorp/go-tfe"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-exec/tfexec"
	"go.uber.org/zap"
	"io"
	"os"
	"strings"
)

var logger *zap.Logger

func init() {
	logger, _ = zap.NewProduction()
}

func main() {
	directory := flag.String("directory", "", "The working directory")
	//parallelism := flag.Int("parallelism", 10, "Threads to run")
	version := flag.String("version", "latest", "Tofu version to run")
	logAddress := flag.String("log-address", "", "Endpoint for streaming logs")

	operation := os.Args[1]

	_, err := chushi.New(tfe.DefaultConfig())
	if err != nil {
		logger.Fatal("failed to initialize chushi SDK", zap.Error(err))
	}
	ctx := context.Background()

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
	installer := install.NewInstaller()

	var source src.Source
	if tofuVersion == "latest" {
		source = &releases.LatestVersion{
			Product: product.Tofu,
		}
	} else {
		v, err := version.NewVersion(tofuVersion)
		if err != nil {
			return nil, err
		}
		source = &releases.ExactVersion{
			Version: v,
			Product: product.Tofu,
		}
	}

	installation, err := installer.Ensure(context.TODO(), []src.Source{source})
	if err != nil {
		return nil, err
	}
	return tfexec.NewTerraform(workingDirectory, installation)
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
