module github.com/chushi-io/runner

go 1.22.5

replace github.com/opentofu/tofu-exec => github.com/Magnitus-/tofu-exec v0.0.0-20231020041209-c1eb82960af7

// Replace directives for testing
//replace github.com/opentofu/tofu-exec => /app/tofu-exec
//replace github.com/opentofu/tofu-exec => /Users/rwittman/Repos/chushi-io/runner/tofu-exec

require (
	github.com/hashicorp/terraform-json v0.22.1
	github.com/opentofu/tofu-exec v0.0.0-00010101000000-000000000000
	go.uber.org/zap v1.27.0
)

require (
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/hashicorp/hc-install v0.6.4 // indirect
	github.com/stretchr/testify v1.9.0 // indirect
	github.com/zclconf/go-cty v1.14.4 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/crypto v0.27.0 // indirect
	golang.org/x/mod v0.21.0 // indirect
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/text v0.18.0 // indirect
)
