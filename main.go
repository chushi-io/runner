package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/opentofu/tofu-exec/tfexec"
	"go.uber.org/zap"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	logger *zap.Logger
)

var DEFAULT_TOFU_VERSION = "1.8.2"

func init() {
	logger, _ = zap.NewProduction()
}

var (
	planOnly                      bool
	targets                       string
	destroy                       bool
	logUploadUrl                  string
	hostedPlanUploadUrl           string
	hostedJsonPlanUploadUrl       string
	hostedStructuredJsonUploadUrl string
	redactedJsonUploadurl         string
)

type printferAdapter struct {
	l *zap.Logger
}

func (p printferAdapter) Printf(format string, v ...interface{}) {
	p.l.Info(fmt.Sprintf(format, v...))
}

func main() {
	directory := flag.String("directory", "", "The working directory")
	//parallelism := flag.Int("parallelism", 10, "Threads to run")
	version := flag.String("version", "latest", "Tofu version to run")
	_ = flag.String("log-stream-url", "", "Endpoint for streaming logs")
	_ = flag.String("run-id", "", "ID of the current run")
	debug := flag.Bool("debug", false, "Log debug statements")

	// Operation specific flags, these are bound to vars
	flag.BoolVar(&planOnly, "plan-only", false, "Only run a plan operation")
	flag.StringVar(&targets, "targets", "", "Target addresses")
	flag.BoolVar(&destroy, "destroy", false, "Run destroy operation")

	// Upload URLs
	flag.StringVar(&logUploadUrl, "log-upload-url", "", "URL to upload logs to")
	flag.StringVar(&hostedPlanUploadUrl, "hosted-plan-upload-url", "", "URL to upload JSON plan")
	flag.StringVar(&hostedJsonPlanUploadUrl, "hosted-json-plan-upload-url", "", "URL to upload the plan file")
	flag.StringVar(&hostedStructuredJsonUploadUrl, "hosted-structured-json-upload-url", "", "URL to upload the structured JSON file")
	flag.StringVar(&redactedJsonUploadurl, "redacted-json-upload-url", "", "URL to upload redacted JSON output")

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

	//tf.SetLogger(printferAdapter{l: logger})
	logger.Debug("setting up log adapter")
	logAdapter := &logUploadAdapter{logUploadUrl: logUploadUrl}
	writer := io.MultiWriter(logAdapter, os.Stdout)

	logger.Debug("setting up execution environment")
	if err = setup(tf); err != nil {
		logger.Fatal("failed to setup execution", zap.Error(err))
	}

	logger.Debug("initializing tofu")
	if err = tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		logger.Fatal("failed to initialize tofu", zap.Error(err))
	}
	logger.Debug("tofu initialized")

	switch operation {
	case "plan":
		err = opPlan(ctx, writer, tf)
		if logUploadErr := logAdapter.Flush(); logUploadErr != nil {
			logger.Error("failed uploading log flush", zap.Error(logUploadErr))
		}
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

	return nil
}

func opPlan(ctx context.Context, writer io.Writer, tf *tfexec.Terraform) error {
	client := &http.Client{}

	// Mark plan as running
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

	if !hasChanges {
		return nil
	}

	// If we don't reset the writers, further commands on our
	// handle will pipe the output to plan logs, which we don't want
	tf.SetStdout(io.Discard)
	tf.SetStderr(os.Stderr)

	// Upload the binary plan file
	planBinaryFile := filepath.Join(tf.WorkingDir(), "tfplan")
	// Upload the plan binary file
	data, err := os.Open(planBinaryFile)
	if err != nil {
		return err
	}

	// Upload the redacted JSON plan file
	plan, err := tf.ShowPlanFile(ctx, planBinaryFile)
	if err != nil {
		return err
	}
	marshalledRedactedPlan, err := json.Marshal(plan)
	if err != nil {
		return err
	}

	// Upload the JSON plan
	providerSchemas, err := tf.ProvidersSchema(context.TODO())
	if err != nil {
		return err
	}
	jsonPlan := &JSONPlan{
		ProviderSchemas:       providerSchemas.Schemas,
		ProviderFormatVersion: providerSchemas.FormatVersion,
		OutputChanges:         plan.OutputChanges,
		ResourceChanges:       plan.ResourceChanges,
		ResourceDrift:         plan.ResourceDrift,
		RelevantAttributes:    plan.RelevantAttributes,
	}
	marshalledJsonPlan, err := json.Marshal(jsonPlan)
	if err != nil {
		return err
	}

	// Upload all of the required files to our server
	var wg sync.WaitGroup

	// TODO: We probably want to catch the error here
	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("uploading plan file")
		if err = uploadFileToUrl(client, hostedPlanUploadUrl, data); err != nil {
			logger.Error("failed uploading plan file", zap.Error(err))
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("uploading hosted json file")
		if err = uploadFileToUrl(client, hostedJsonPlanUploadUrl, bytes.NewReader(marshalledJsonPlan)); err != nil {
			logger.Error("failed uploading hosted json file", zap.Error(err))
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("uploading redacted json file")
		if err = uploadFileToUrl(client, redactedJsonUploadurl, bytes.NewReader(marshalledRedactedPlan)); err != nil {
			logger.Error("failed uploading redacted json file", zap.Error(err))
		}
	}()

	wg.Wait()
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

type JSONPlan struct {
	PlanFormatVersion  string                     `json:"plan_format_version"`
	OutputChanges      map[string]*tfjson.Change  `json:"output_changes"`
	ResourceChanges    []*tfjson.ResourceChange   `json:"resource_changes"`
	ResourceDrift      []*tfjson.ResourceChange   `json:"resource_drift"`
	RelevantAttributes []tfjson.ResourceAttribute `json:"relevant_attributes"`

	ProviderFormatVersion string                            `json:"provider_format_version"`
	ProviderSchemas       map[string]*tfjson.ProviderSchema `json:"provider_schemas"`
}

type logUploadAdapter struct {
	logUploadUrl string
	lines        [][]byte
}

func (l *logUploadAdapter) Write(p []byte) (int, error) {
	l.lines = append(l.lines, p)
	// Disable streaming for now
	//if err := l.upload(); err != nil {
	//	fmt.Println(err)
	//	return 0, err
	//}
	return len(p), nil
}

func (l *logUploadAdapter) Flush() error {
	return l.upload()
}

func (l *logUploadAdapter) upload() error {
	var buf bytes.Buffer
	for _, line := range l.lines {
		buf.Write(line)
	}
	req, err := http.NewRequest("PUT", l.logUploadUrl, &buf)
	if err != nil {
		log.Fatal(err)
	}

	req.Header.Set("Content-Type", "text/plain")
	client := &http.Client{}

	res, err := client.Do(req)
	if err != nil {
		return err
	}

	if res.StatusCode != http.StatusCreated {
		return errors.New(fmt.Sprintf("response code: %d", res.StatusCode))
	}

	return nil
}

func uploadFileToUrl(client *http.Client, url string, data io.Reader) error {
	req, err := http.NewRequest("PUT", url, data)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "text/plain")
	res, err := client.Do(req)
	if err != nil {
		return err
	}

	if res.StatusCode != http.StatusCreated {
		return errors.New(fmt.Sprintf("response code: %d", res.StatusCode))
	}

	return nil
}
