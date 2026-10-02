package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/daemon"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

func newDaemonCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: i18n.T("cli.daemon.short"),
		Long:  i18n.T("cli.daemon.long"),
	}

	cmd.AddCommand(
		newDaemonRunCmd(app),
		newDaemonInstallCmd(app),
		newDaemonUninstallCmd(app),
	)
	return cmd
}

func newDaemonRunCmd(app *App) *cobra.Command {
	var (
		loopbackAddr    string
		disableLoopback bool
		allowedOrigins  []string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: i18n.T("cli.daemon.run_short"),
		Long:  i18n.T("cli.daemon.run_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			// 只在用户真的传了 --lang 时才把语言交给内核：
			// 无条件传会让"用户通过接口改成 en"在每次重启后被改回默认值。
			var lang i18n.Lang
			if cmd.Root().PersistentFlags().Changed("lang") {
				lang = app.lang
			}

			d := daemon.New(daemon.Options{
				Paths:           app.paths,
				LogHandler:      app.logHandler,
				Lang:            lang,
				LoopbackAddr:    loopbackAddr,
				DisableLoopback: disableLoopback,
				AllowedOrigins:  allowedOrigins,
			})

			return d.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&loopbackAddr, "loopback", "",
		i18n.T("cli.daemon.flag_listen"))
	cmd.Flags().BoolVar(&disableLoopback, "no-loopback", false,
		i18n.T("cli.daemon.flag_no_tcp"))
	cmd.Flags().StringSliceVar(&allowedOrigins, "allow-origin", nil,
		i18n.T("cli.daemon.flag_origins"))

	return cmd
}

// errNotImplementedYet 统一"里程碑未到"的错误文案风格。
func errNotImplementedYet(what, milestone string) error {
	return fmt.Errorf(i18n.T("cli.daemon.stub_prefix"), what+" "+milestone)
}

func newDaemonInstallCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: i18n.T("cli.daemon.install_short"),
		Long:  i18n.T("cli.daemon.install_long"),
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errNotImplementedYet(i18n.T("cli.daemon.backend_install"), "M5")
		},
	}
}

func newDaemonUninstallCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: i18n.T("cli.daemon.uninstall_short"),
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errNotImplementedYet(i18n.T("cli.daemon.backend_uninstall"), "M5")
		},
	}
}
