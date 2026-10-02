package cli

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/version"
)

// versionOutput 是 `isc version --json` 的输出结构。
//
// 单独定义而不是直接序列化内部结构：CLI 的输出是**对外契约**的一部分，
// 下游脚本会依赖字段名，因此它必须有意识地设计，而不是随内部结构漂移。
type versionOutput struct {
	Version    string `json:"version"`
	APIVersion string `json:"api_version"`
	Commit     string `json:"commit,omitempty"`
	BuildTime  string `json:"build_time,omitempty"`
	GoVersion  string `json:"go_version"`
	Platform   string `json:"platform"`
}

func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: i18n.T("cli.version.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v := versionOutput{
				Version:    version.Version,
				APIVersion: version.APIVersion,
				Commit:     version.Commit,
				BuildTime:  version.BuildTime,
				GoVersion:  runtime.Version(),
				Platform:   runtime.GOOS + "/" + runtime.GOARCH,
			}

			if app.jsonOut {
				enc := json.NewEncoder(app.out)
				enc.SetIndent("", "  ")
				return enc.Encode(v)
			}

			_, _ = fmt.Fprintf(app.out, "%s\n", i18n.T("cli.version_header"))
			_, _ = fmt.Fprintf(app.out, "  %-12s %s\n", "version", v.Version)
			_, _ = fmt.Fprintf(app.out, "  %-12s %s\n", "api", v.APIVersion)
			if v.Commit != "" {
				_, _ = fmt.Fprintf(app.out, "  %-12s %s\n", "commit", v.Commit)
			}
			if v.BuildTime != "" {
				_, _ = fmt.Fprintf(app.out, "  %-12s %s\n", "built", v.BuildTime)
			}
			_, _ = fmt.Fprintf(app.out, "  %-12s %s\n", "go", v.GoVersion)
			_, _ = fmt.Fprintf(app.out, "  %-12s %s\n", "platform", v.Platform)
			return nil
		},
	}
}
