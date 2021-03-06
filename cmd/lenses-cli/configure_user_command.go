package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/landoop/lenses-go"

	"github.com/kataras/survey"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newGetConfigurationContextsCommand())
	rootCmd.AddCommand(newConfigurationContextCommand())
	rootCmd.AddCommand(newConfigureCommand())
	rootCmd.AddCommand(newLoginCommand())
	rootCmd.AddCommand(newGetUserInfoCommand())
	// remove `logout` command (at least for the moment) rootCmd.AddCommand(newLogoutCommand())
	rootCmd.AddCommand(newGetLicenseInfoCommand())
}

func isValidConfigurationContext(name string) bool {
	currentContext := configManager.config.CurrentContext
	configManager.setCurrent(name)
	isValid := setupClient() == nil
	configManager.setCurrent(currentContext)
	return isValid
}

func printConfigurationContext(cmd *cobra.Command, name string) bool {
	currentContextName := configManager.config.CurrentContext
	c, ok := configManager.config.Contexts[name]
	if !ok {
		return false // this should never happen.
	}
	c.FormatHost()
	cfg := *c

	isValid := isValidConfigurationContext(name)

	info := "valid"
	if !isValid {
		info = "invalid"
	}

	if cfg.Password != "" {
		cfg.Password = "****"
	}
	if cfg.Token != "" {
		cfg.Token = "****"
	}

	if name == currentContextName {
		info += ", current"
	}

	fmt.Fprintf(cmd.OutOrStdout(), "%s [%s]\n", name, info)
	printJSON(cmd, cfg)

	return isValid
}

func showOptionsForConfigurationContext(cmd *cobra.Command, name string) error {
	var action string

	if err := survey.AskOne(&survey.Select{
		Message: fmt.Sprintf("Would you like to skip, edit or delete the '%s' invalid configuration context?", name),
		Options: []string{"skip", "edit", "delete"},
	}, &action, nil); err != nil {
		return err
	}

	if action == "skip" {
		return nil
	}

	if action == "delete" {
		deleteCmd := newDeleteConfigurationContextCommand()
		deleteCmd.SetArgs([]string{name})
		if err := deleteCmd.Execute(); err != nil {
			return err
		}

		return nil
	}

	if action == "edit" {
		editCmd := newUpdateConfigurationContextCommand()
		editCmd.SetArgs([]string{name})
		if err := editCmd.Execute(); err != nil {
			return err
		}
	}

	return nil
}

// Note that configure will never be called if home configuration is already exists, even if `lenses-cli configure`,
// this is an expected behavior to prevent any actions by mistakes from the user.
func newGetConfigurationContextsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "contexts",
		Short:         "Print and validate (through calls to the servers) all the available contexts from the configuration file",
		Example:       exampleString(`contexts`),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			for name := range configManager.config.Contexts {
				if !printConfigurationContext(cmd, name) {
					if !silent {
						showOptionsForConfigurationContext(cmd, name)
					}
				}
			}
			return nil
		},
	}

	canBeSilent(cmd)

	return cmd
}

func newConfigurationContextCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "context",
		Short:         "Print the current context or modify or delete a configuration context using the update and delete subcommands",
		Example:       exampleString(`context`),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// normally the cli would throw "client: credentials missing or invalid" if the current context's configuration
			// are invalid, but in the case of "context" command, we skip that setup on the root command.
			if !configManager.currentContextExists() {
				return fmt.Errorf("current context does not exist, please use the `configure` command first")
			}
			name := configManager.config.CurrentContext
			if !printConfigurationContext(cmd, name) {
				if !silent {
					showOptionsForConfigurationContext(cmd, name)
				}
			}
			return nil
		},
	}

	canBeSilent(root)

	root.AddCommand(newUpdateConfigurationContextCommand())
	root.AddCommand(newDeleteConfigurationContextCommand())

	return root
}

func newDeleteConfigurationContextCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "delete",
		Short:         "Delete a configuration context",
		Example:       exampleString(`context delete context_name`),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("one argument is required for the context name")
			}

			name := args[0]
			removeContextWillChangeContext := configManager.config.CurrentContext == name
			deleted := configManager.removeContext(name)

			if !deleted {
				// failed when no found this context or if we can't upgrade to another one.
				return echo(cmd, "unable to delete context '%s', at least one more valid context should be present", name)
			}

			succMsg := fmt.Sprintf("'%s' context deleted", name)

			if removeContextWillChangeContext {
				newCurrentContext := configManager.config.CurrentContext
				succMsg = fmt.Sprintf("%s, current context set to '%s'", succMsg, newCurrentContext)
			}

			return echo(cmd, succMsg)

		},
	}

	canBeSilent(cmd)

	return cmd
}

func newUpdateConfigurationContextCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "set",
		Aliases:       []string{"edit", "update", "create", "add"},
		Short:         "Edit an existing or add a configuration context, similar to 'configure --context=context_name --reset' but without banner and this one saves the configuration to the default location",
		Example:       exampleString(`context edit context_name`),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("one argument is required for the context name")
			}

			name := args[0]

			configureCmd := newConfigureCommand()
			rootCmd.Flag("context").Value.Set(name)
			configureCmd.Flag("reset").Value.Set("true")
			// these wil disable banner and location save, note that if --file is there then it will take that, otherwise the default $HOME/.lenses/lenses-cli.yml.
			configureCmd.Flag("no-banner").Value.Set("true")
			configureCmd.Flag("default-location").Value.Set("true")
			if err := configureCmd.Execute(); err != nil {
				return err
			}

			if isValidConfigurationContext(name) {
				return echo(cmd, "%s was successfully validated and saved, it is the current context now", name)
			}

			retry := true
			if err := survey.AskOne(&survey.Confirm{
				Message: fmt.Sprintf("%s is still invalid, do you mind to retry fixing it?", name),
				Default: true,
			}, &retry, nil); err != nil {
				return err
			}

			if retry {
				newCmd := newUpdateConfigurationContextCommand()
				newCmd.SetArgs(args)
				return newCmd.Execute()
			}

			return nil
		},
	}

	canBeSilent(cmd)

	return cmd
}

// Note that configure will never be called if home configuration is already exists, even if `lenses-cli configure`,
// this is an expected behavior to prevent any actions by mistakes from the user.
func newConfigureCommand() *cobra.Command {
	var (
		reset       bool
		noBanner    bool // if true doesn't print the banner (useful for running inside other commands).
		defLocation bool // if true doesn't asks for location to save (useful for running inside other commands).
	)

	cmd := &cobra.Command{
		Use:           "configure",
		Short:         "Setup your environment for extensive CLI use. Create and save the required CLI configuration and client credentials",
		Example:       exampleString(`configure`),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !configManager.isValid() || reset {
				// This is the only command and place the user has direct interaction with the CLI
				// and it's not used by a third-party tool because of the survey.
				// So, print our "banner" :)
				if !noBanner {
					fmt.Fprintln(cmd.OutOrStdout(), `
	 ___      _______  __    _  _______  _______  _______ 
	|   |    |       ||  |  | ||       ||       ||       |
	|   |    |    ___||   |_| ||  _____||    ___||  _____|
	|   |    |   |___ |       || |_____ |   |___ | |_____ 
	|   |___ |    ___||  _    ||_____  ||    ___||_____  |
	|       ||   |___ | | |   | _____| ||   |___  _____| |
	|_______||_______||_|  |__||_______||_______||_______|
					   `)
				}

				currentConfig := configManager.getCurrent()

				qs := []*survey.Question{
					{
						Name: "host",
						Prompt: &survey.Input{
							Message: "Host",
							Default: currentConfig.Host,
							Help:    "This is your lenses box host full address, including the schema and the port. The address that this Client will be connected to.",
						},
						Validate: survey.Required,
					},
					{
						Name: "user",
						Prompt: &survey.Input{
							Message: "User",
							Default: currentConfig.User,
							Help:    "This is the user credential used for gain access to the API.",
						},
						Validate: survey.Required,
					},
					{
						Name: "password",
						Prompt: &survey.Password{
							Message: "Password",
							Help:    "This is the user's password credential, necessary to gain access to the API.",
						},
						Validate: survey.Required,
					},
					{
						Name: "debug",
						Prompt: &survey.Confirm{
							Message: "Enable debug mode?",
							Default: currentConfig.Debug,
						},
					},
				}

				if err := survey.Ask(qs, currentConfig); err != nil {
					return err
				} // else continue by saving the result to the desired system filepath.

				// if already saved once and want to add more contexts, then don't ask for system path.
				if configManager.config.CurrentContext != "" && len(configManager.config.Contexts) > 0 {
					defLocation = true
				}

				if configManager.filepath == "" && !defLocation { // if no --config is provided then ask.
					if err := survey.AskOne(&survey.Input{
						Message: "Save configuration file to",
						Default: defaultConfigFilepath,
						Help:    "This is the system filepath to save the configuration which includes the credentials",
					}, &configManager.filepath, nil); err != nil {
						return err
					}
				}

			} else {
				nFlags := cmd.Root().Flags().NFlag()
				if nFlags == 0 || (nFlags == 1 && cmd.Root().Flag("context").Changed) || (nFlags <= 2 && cmd.Root().Flag("config").Changed) {
					// flags given like --user and --pass and --host, then we don't want to save anything,
					// user may need to re-configure, give a note about the --reset flag.
					return fmt.Errorf("configuration already exists, try 'configure --reset' instead")
				}
			}

			return configManager.save()
		},
	}

	cmd.Flags().BoolVar(&reset, "reset", false, "reset the current configuration")
	cmd.Flags().BoolVar(&noBanner, "no-banner", false, "disables the banner output")
	cmd.Flags().BoolVar(&defLocation, "default-location", false, "will not ask for the location to save on, the result will be saved to the $HOME/.lenses/lenses-cli.yml")
	return cmd
}

func toHash(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

func encryptAES(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	out := make([]byte, aes.BlockSize+len(data))
	iv := out[:aes.BlockSize]
	encrypted := out[aes.BlockSize:]

	if _, err = io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}

	stream := cipher.NewCFBEncrypter(block, iv)
	stream.XORKeyStream(encrypted, data)

	return out, nil
}

func decryptAES(key, h []byte) ([]byte, error) {
	iv := h[:aes.BlockSize]
	h = h[aes.BlockSize:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	str := cipher.NewCFBDecrypter(block, iv)
	str.XORKeyStream(h, h)

	return h, nil
}

func encryptString(plain string, keyBase string) (string, error) {
	key := toHash(keyBase)
	encrypted, err := encryptAES(key, []byte(plain))
	if err != nil {
		return "", err
	}

	return base64.URLEncoding.EncodeToString(encrypted), nil
}

func decryptString(encryptedRaw string, keyBase string) (plainTextString string, err error) {
	encrypted, err := base64.URLEncoding.DecodeString(encryptedRaw)
	if err != nil {
		return "", err
	}

	if len(encrypted) < aes.BlockSize {
		return "", fmt.Errorf("short cipher, min len: 16")
	}

	decrypted, err := decryptAES(toHash(keyBase), encrypted)
	if err != nil {
		return "", err
	}

	return string(decrypted), nil
}

var defaultConfigFilepath = filepath.Join(lenses.DefaultConfigurationHomeDir, "lenses-cli.yml")

func encryptPassword(cfg *lenses.Configuration) error {
	if cfg.Password == "" {
		return fmt.Errorf("empty password")
	}

	p, err := encryptString(cfg.Password, cfg.Host)
	if err != nil {
		return err
	}

	cfg.Password = p
	return nil
}

func decryptPassword(cfg *lenses.Configuration) {
	p, _ := decryptString(cfg.Password, cfg.Host)
	cfg.Password = p
}

func newLoginCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:              "login",
		Short:            "Login, generate the access token using the generated configuration via the 'configure' command. ",
		Example:          exampleString(`login`),
		SilenceErrors:    true,
		TraverseChildren: true,
		Hidden:           true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := setupClient(); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			signedUser := client.User()
			fmt.Fprintf(out, "Welcome %s[%s],\ntype 'help' to learn more about the available commands or 'exit' to terminate.\n",
				signedUser.Name, strings.Join(signedUser.Roles, ", "))
			// read the input pipe, on each read its buffer consumed, so loop 'forever' here.
			streamReader := bufio.NewReader(os.Stdin)
			for {
				// add the 'ready to type' signal for the user.
				fmt.Fprint(out, "> ")
				line, err := streamReader.ReadString('\n')
				if err != nil {
					return err // exit on first failure.
				}

				// remove any last \r\n.
				line = strings.TrimRight(line, "\r\n")

				// if "exit" then exit now.
				if line == "exit" {
					os.Exit(0)
				}

				cms := strings.Split(line, " ")

				/* Remember: why we do this "cP"?:
				   if not then:
				    processors --no-pretty
				    and after
				    processors
				    will keep the --no-pretty flag to true without be able to change it via --no-pretty=false.

				    With the clone solution we still remember the flags(very important) but they can be changed if needed.
				*/

				// parse the line (as slice of strings) in order to take the command and the flags from it.
				cP, flags, err := rootCmd.Find(cms)
				if err != nil {
					fmt.Fprintln(out, err)
					continue
				}

				c := *cP
				commandName := c.Name()

				// check if "login" or "configure" commands, these cannot be run in the terminal session
				// for obvious reasons.
				if commandName == "login" || commandName == "configure" {
					fmt.Fprintln(out, "unable to run inside a started session")
					continue
				}

				// parse the flags found by the `Find`.
				if err = c.ParseFlags(flags); err != nil {
					fmt.Fprintln(out, err)
					continue
				}

				// see if we have arguments to set, arguments come after the flags.
				var cArgs []string
				if stopFlags := len(flags) + 1; len(cms) > stopFlags {
					cArgs = cms[1:stopFlags]
				}

				// run the command.
				c.DisableFlagParsing = true
				c.DisableFlagsInUseLine = true
				c.SetArgs(cArgs)

				if c.Run == nil && c.RunE == nil {
					// propably report as bug if this will happen ever.
					fmt.Fprintln(out, "command is unable to run")
					continue
				}

				if c.Run != nil {
					c.Run(&c, cArgs)
				} else if err = c.RunE(&c, cArgs); err != nil {
					fmt.Fprintln(out, err)
					// don't break this yet, let it to print an extra line if it was caused by the child command itself,
					// also the "logout" command can check for that error as well.
				}

				// if command was "logout" then exit.
				if commandName == logoutCmdName {
					if err != nil {
						os.Exit(1)
					}
					os.Exit(0)
				}

				// a new line on succeed operations.
				fmt.Fprintln(out)
			}

		}}

	return cmd
}

func newGetUserInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:              "user",
		Short:            "Print some information about the authenticated logged user such as the given roles given by the lenses administrator",
		Example:          exampleString("user"),
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if user := client.User(); user.ID != "" {
				// if logged in using the user password, then we have those info,
				// let's print it as well.
				return printJSON(cmd, user)
			}
			return nil
		},
	}

	canPrintJSON(cmd)

	return cmd
}

const logoutCmdName = "logout"

// func newLogoutCommand() *cobra.Command {
// 	cmd := &cobra.Command{
// 		Use:              logoutCmdName,
// 		Short:            "Revoke the access token",
// 		Example:          exampleString(logoutCmdName),
// 		TraverseChildren: true,
// 		RunE: func(cmd *cobra.Command, args []string) error {
// 			if err := client.Logout(); err != nil {
// 				return err // first re-voke the access token.
// 			}

// 			// after remove the token from the configuration.
// 			currentConfig.Token = ""
// 			return saveConfiguration()
// 		},
// 	}

// 	return cmd
// }

func newGetLicenseInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:              "license",
		Short:            "Print the license information for the connected lenses box",
		Example:          exampleString("license"),
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			lc, err := client.GetLicenseInfo()
			if err != nil {
				return err
			}

			return printJSON(cmd, lc)
		},
	}

	canPrintJSON(cmd)

	return cmd
}
