package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/credstore"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage and verify API credentials",
	Long: `Manage PTV API credentials.

Credentials are resolved in priority order: environment variables
(PTV_API_KEY, PTV_API_USERID), then the OS-native secret store (macOS
Keychain, Windows Credential Manager, Linux Secret Service), then a .env
file in the working directory.

Use 'ptv auth login' to store credentials securely in the OS secret store.`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Securely store PTV credentials in the OS secret store",
	RunE: func(cmd *cobra.Command, args []string) error {
		devID, err := promptLine("PTV User/Developer ID: ")
		if err != nil {
			return err
		}
		apiKey, err := promptSecret("PTV API Key: ")
		if err != nil {
			return err
		}
		devID = strings.TrimSpace(devID)
		apiKey = strings.TrimSpace(apiKey)
		if devID == "" || apiKey == "" {
			return fmt.Errorf("both User/Developer ID and API Key are required")
		}

		// Verify before persisting.
		client := ptvapi.New(defaultBaseURL(), apiKey, devID)
		if _, err := client.RouteTypes(ctx()); err != nil {
			return fmt.Errorf("credentials rejected by PTV API: %w", err)
		}

		if err := credstore.Save(credstore.Credentials{APIKey: apiKey, DevID: devID}); err != nil {
			return fmt.Errorf("storing credentials in OS keyring: %w", err)
		}
		fmt.Println("Credentials verified and stored securely in the OS secret store.")
		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored credentials from the OS secret store",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := credstore.Delete(); err != nil {
			return fmt.Errorf("removing credentials: %w", err)
		}
		fmt.Println("Stored credentials removed from the OS secret store.")
		return nil
	},
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show which credential source is active",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			fmt.Println("No credentials configured.")
			fmt.Println("Run 'ptv auth login' to store them securely.")
			return nil
		}
		fmt.Printf("Credentials configured (source: %s)\n", cfg.CredentialSource)
		fmt.Printf("User/Developer ID: %s\n", cfg.DevID)
		return nil
	},
}

var authCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify PTV API credentials with a signed request",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, cfg, err := loadClient()
		if err != nil {
			return err
		}
		resp, err := client.RouteTypes(ctx())
		if err != nil {
			return fmt.Errorf("credential check failed: %w", err)
		}
		if flagJSON {
			return printJSON(resp)
		}
		fmt.Printf("Credentials OK (devid %s, source %s)\n", cfg.DevID, cfg.CredentialSource)
		fmt.Printf("API version %s, health %d\n", resp.Status.Version, resp.Status.Health)
		fmt.Printf("Available modes: ")
		for i, rt := range resp.RouteTypes {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Print(rt.RouteTypeName)
		}
		fmt.Println()
		return nil
	},
}

// stdinReader is shared across prompts so buffered reads don't discard input.
var stdinReader = bufio.NewReader(os.Stdin)

// promptLine prints a prompt and reads a line from stdin.
func promptLine(prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// promptSecret reads a secret without echoing it when stdin is a terminal.
func promptSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return promptLine("")
}

func init() {
	authCmd.AddCommand(authLoginCmd, authLogoutCmd, authStatusCmd, authCheckCmd)
	rootCmd.AddCommand(authCmd)
}
