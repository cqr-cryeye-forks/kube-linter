package lint

import (
	"fmt"
	"os"

	"golang.stackrox.io/kube-linter/internal/flagutil"
	"golang.stackrox.io/kube-linter/pkg/builtinchecks"
	"golang.stackrox.io/kube-linter/pkg/checkregistry"
	"golang.stackrox.io/kube-linter/pkg/command/common"
	"golang.stackrox.io/kube-linter/pkg/config"
	"golang.stackrox.io/kube-linter/pkg/configresolver"
	"golang.stackrox.io/kube-linter/pkg/lintcontext"
	"golang.stackrox.io/kube-linter/pkg/run"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	plainTemplateStr = `KubeLinter {{.Summary.KubeLinterVersion}}

{{range .Reports}}
{{- .Object.Metadata.FilePath | bold}}: (object: {{.Object.GetK8sObjectName | bold}}) {{.Diagnostic.Message | red}} (check: {{.Check | yellow}}, remediation: {{.Remediation | yellow}})

{{else}}No lint errors found!
{{end -}}
`
)

var (
	plainTemplate = common.MustInstantiatePlainTemplate(plainTemplateStr, nil)

	formatters = common.Formatters{
		Formatters: map[common.FormatType]common.FormatFunc{
			common.JSONFormat:  common.FormatJSON,
			common.SARIFFormat: formatLintSarif,
			common.PlainFormat: plainTemplate.Execute,
		},
	}
)

// Command is the command for the lint command.
func Command() *cobra.Command {
	var configPath string
	var verbose bool
	format := flagutil.NewEnumFlag("Output format", formatters.GetEnabledFormatters(), common.PlainFormat)

	v := viper.New()

	c := &cobra.Command{
		Use:   "lint",
		Args:  cobra.MinimumNArgs(1),
		Short: "Lint Kubernetes YAML files and Helm charts",
		RunE: func(cmd *cobra.Command, args []string) error {
			checkRegistry := checkregistry.New()
			if err := builtinchecks.LoadInto(checkRegistry); err != nil {
				return err
			}

			// Load Configuration
			cfg, err := config.Load(v, configPath)
			if err != nil {
				return errors.Wrap(err, "failed to load config")
			}

			if err := configresolver.LoadCustomChecksInto(&cfg, checkRegistry); err != nil {
				return err
			}
			enabledChecks, err := configresolver.GetEnabledChecksAndValidate(&cfg, checkRegistry)
			if err != nil {
				return err
			}
			if len(enabledChecks) == 0 {
				fmt.Fprintln(os.Stderr, "Warning: no checks enabled.")
				return nil
			}
			lintCtxs, err := lintcontext.CreateContexts(args...)
			if err != nil {
				return err
			}
			if verbose {
				for _, lintCtx := range lintCtxs {
					for _, invalidObj := range lintCtx.InvalidObjects() {
						fmt.Fprintf(os.Stderr, "Warning: failed to load object from %s: %v\n", invalidObj.Metadata.FilePath, invalidObj.LoadErr)
					}
				}
			}
			var atLeastOneObjectFound bool
			for _, lintCtx := range lintCtxs {
				if len(lintCtx.Objects()) > 0 {
					atLeastOneObjectFound = true
					break
				}
			}
			if !atLeastOneObjectFound {
				fmt.Fprintln(os.Stderr, "Warning: no valid objects found.")
				return nil
			}
			result, err := run.Run(lintCtxs, checkRegistry, enabledChecks)
			if err != nil {
				return err
			}

			formatter, err := formatters.FormatterByType(format.String())
			if err != nil {
				return err
			}

			file, _ := os.OpenFile("output.json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
			err = formatter(file, result)
			if err != nil {
				return errors.Wrap(err, "output saving failed")
			}

			err = formatter(os.Stdout, result)
			if err != nil {
				return errors.Wrap(err, "output formatting failed")
			}

			if len(result.Reports) > 0 {
				err = errors.Errorf("found %d lint errors", len(result.Reports))
			}
			return err
		},
	}

	c.Flags().StringVar(&configPath, "config", "", "Path to config file")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging")
	c.Flags().Var(format, "format", format.Usage())

	config.AddFlags(c, v)
	return c
}
