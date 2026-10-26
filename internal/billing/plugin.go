package billing

import (
	"context"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(usagePlugin{})
}

// usagePlugin feeds successful usage records into the process-wide billing store.
type usagePlugin struct{}

func (usagePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	Default().HandleUsage(ctx, record)
}
