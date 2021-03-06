package command_parser

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"code.cloudfoundry.org/cli/cf/cmd"
	"code.cloudfoundry.org/cli/command"
	"code.cloudfoundry.org/cli/command/common"
	"code.cloudfoundry.org/cli/command/flag"
	"code.cloudfoundry.org/cli/command/translatableerror"
	"code.cloudfoundry.org/cli/util/configv3"
	"code.cloudfoundry.org/cli/util/ui"
	"github.com/jessevdk/go-flags"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
)

const UnknownCommandCode = -666

// TODO Unwind this code, remove specific edge case just for v2v3 app command
const switchToV2 = -3

var ErrFailed = errors.New("command failed")
var ParseErr = errors.New("incorrect type for arg")

type UI interface {
	DisplayError(err error)
	DisplayWarning(template string, templateValues ...map[string]interface{})
	DisplayText(template string, templateValues ...map[string]interface{})
	FlushDeferred()
}

type DisplayUsage interface {
	DisplayUsage()
}

type TriggerLegacyMain interface {
	LegacyMain()
	error
}

func ParseCommandFromArgs(args []string) int {
	exitStatus := parse(args, &common.Commands)
	if exitStatus == switchToV2 {
		exitStatus = parse(args, &common.FallbackCommands)
	}
	return exitStatus
}

func executionWrapper(cmd flags.Commander, args []string) error {
	cfConfig, commandUI, err := getCFConfigAndCommandUIObjects()
	if err != nil {
		return err
	}
	defer commandUI.FlushDeferred()

	err = preventExtraArgs(args)
	if err != nil {
		return handleError(err, commandUI)
	}

	err = cfConfig.CreatePluginHome()
	if err != nil {
		return handleError(err, commandUI)
	}

	defer func() {
		configWriteErr := cfConfig.WriteConfig()
		if configWriteErr != nil {
			fmt.Fprintf(os.Stderr, "Error writing config: %s", configWriteErr.Error())
		}
	}()

	if extendedCmd, ok := cmd.(command.ExtendedCommander); ok {
		log.SetOutput(os.Stderr)
		log.SetLevel(log.Level(cfConfig.LogLevel()))

		err = extendedCmd.Setup(cfConfig, commandUI)
		if err != nil {
			return handleError(err, commandUI)
		}

		return handleError(extendedCmd.Execute(args), commandUI)
	}

	return fmt.Errorf("command does not conform to ExtendedCommander")
}

func getCFConfigAndCommandUIObjects() (*configv3.Config, *ui.UI, error) {
	cfConfig, configErr := configv3.LoadConfig(configv3.FlagOverride{
		Verbose: common.Commands.VerboseOrVersion,
	})
	if configErr != nil {
		if _, ok := configErr.(translatableerror.EmptyConfigError); !ok {
			return nil, nil, configErr
		}
	}
	commandUI, err := ui.NewUI(cfConfig)
	return cfConfig, commandUI, err
}

func handleError(passedErr error, commandUI UI) error {
	if passedErr == nil {
		return nil
	}

	translatedErr := translatableerror.ConvertToTranslatableError(passedErr)

	switch typedErr := translatedErr.(type) {
	case translatableerror.V3V2SwitchError:
		log.Info("Received a V3V2SwitchError - switch to the V2 version of the command")
		return passedErr
	case TriggerLegacyMain:
		if typedErr.Error() != "" {
			commandUI.DisplayWarning("")
			commandUI.DisplayWarning(typedErr.Error())
		}
		cmd.Main(os.Getenv("CF_TRACE"), os.Args)
	case *ssh.ExitError:
		exitStatus := typedErr.ExitStatus()
		if sig := typedErr.Signal(); sig != "" {
			commandUI.DisplayText("Process terminated by signal: {{.Signal}}. Exited with {{.ExitCode}}", map[string]interface{}{
				"Signal":   sig,
				"ExitCode": exitStatus,
			})
		}
		return passedErr
	}

	commandUI.DisplayError(translatedErr)

	if _, ok := translatedErr.(DisplayUsage); ok {
		return ParseErr
	}

	return ErrFailed
}

func handleFlagErrorAndCommandHelp(flagErr *flags.Error, parser *flags.Parser, extraArgs []string, originalArgs []string, commandList interface{}) int {
	switch flagErr.Type {
	case flags.ErrHelp, flags.ErrUnknownFlag, flags.ErrExpectedArgument, flags.ErrInvalidChoice:
		_, commandExists := reflect.TypeOf(common.Commands).FieldByNameFunc(
			func(fieldName string) bool {
				field, _ := reflect.TypeOf(common.Commands).FieldByName(fieldName)
				return parser.Active != nil && parser.Active.Name == field.Tag.Get("command")
			},
		)

		if commandExists && flagErr.Type == flags.ErrUnknownFlag && (parser.Active.Name == "set-env" || parser.Active.Name == "v3-set-env") {
			newArgs := []string{}
			for _, arg := range originalArgs {
				if arg[0] == '-' {
					newArgs = append(newArgs, fmt.Sprintf("%s%s", flag.WorkAroundPrefix, arg))
				} else {
					newArgs = append(newArgs, arg)
				}
			}
			parse(newArgs, commandList)
			return 0
		}

		if flagErr.Type == flags.ErrUnknownFlag || flagErr.Type == flags.ErrExpectedArgument || flagErr.Type == flags.ErrInvalidChoice {
			fmt.Fprintf(os.Stderr, "Incorrect Usage: %s\n\n", flagErr.Error())
		}

		var helpExitCode int
		if commandExists {
			helpExitCode = parse([]string{"help", parser.Active.Name}, commandList)
		} else {
			switch len(extraArgs) {
			case 0:
				helpExitCode = parse([]string{"help"}, commandList)
			case 1:
				if !isOption(extraArgs[0]) || (len(originalArgs) > 1 && extraArgs[0] == "-a") {
					helpExitCode = parse([]string{"help", extraArgs[0]}, commandList)
				} else {
					helpExitCode = parse([]string{"help"}, commandList)
				}
			default:
				if isCommand(extraArgs[0]) {
					helpExitCode = parse([]string{"help", extraArgs[0]}, commandList)
				} else {
					helpExitCode = parse(extraArgs[1:], commandList)
				}
			}
		}

		if helpExitCode > 0 || flagErr.Type == flags.ErrUnknownFlag || flagErr.Type == flags.ErrExpectedArgument || flagErr.Type == flags.ErrInvalidChoice {
			return 1
		}

	case flags.ErrRequired, flags.ErrMarshal:
		fmt.Fprintf(os.Stderr, "Incorrect Usage: %s\n\n", flagErr.Error())
		parse([]string{"help", originalArgs[0]}, commandList)
		return 1
	case flags.ErrUnknownCommand:
		if !isHelpCommand(originalArgs) {
			// TODO Extract handling of unknown commands/suggested  commands out of legacy
			return UnknownCommandCode
		} else {
			helpExitCode := parse([]string{"help", originalArgs[0]}, commandList)
			return helpExitCode
		}
	case flags.ErrCommandRequired:
		if common.Commands.VerboseOrVersion {
			parse([]string{"version"}, commandList)
		} else {
			parse([]string{"help"}, commandList)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unexpected flag error\ntype: %s\nmessage: %s\n", flagErr.Type, flagErr.Error())
	}
	return 0
}

func isCommand(s string) bool {
	_, found := reflect.TypeOf(common.Commands).FieldByNameFunc(
		func(fieldName string) bool {
			field, _ := reflect.TypeOf(common.Commands).FieldByName(fieldName)
			return s == field.Tag.Get("command") || s == field.Tag.Get("alias")
		})

	return found
}

func isHelpCommand(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "--h" {
			return true
		}
	}
	return false
}

func isOption(s string) bool {
	return strings.HasPrefix(s, "-")
}

func parse(args []string, commandList interface{}) int {
	parser := flags.NewParser(commandList, flags.HelpFlag)
	parser.CommandHandler = executionWrapper
	extraArgs, err := parser.ParseArgs(args)
	if err == nil {
		return 0
	} else if _, ok := err.(translatableerror.V3V2SwitchError); ok {
		return switchToV2
	} else if flagErr, ok := err.(*flags.Error); ok {
		return handleFlagErrorAndCommandHelp(flagErr, parser, extraArgs, args, commandList)
	} else if err == ErrFailed {
		return 1
	} else if err == ParseErr {
		fmt.Println()
		parse([]string{"help", args[0]}, commandList)
		return 1
	} else if exitError, ok := err.(*ssh.ExitError); ok {
		return exitError.ExitStatus()
	}

	fmt.Fprintf(os.Stderr, "Unexpected error: %s\n", err.Error())
	return 1
}
