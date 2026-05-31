package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thesammykins/ptv_cli/internal/config"
	"github.com/thesammykins/ptv_cli/internal/credstore"
	"github.com/thesammykins/ptv_cli/internal/gtfsrt"
	"github.com/thesammykins/ptv_cli/internal/ptvapi"
	"github.com/thesammykins/ptv_cli/internal/render"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage and verify API credentials",
	Long: `Manage PTV API credentials.

Credentials are resolved in priority order: environment variables
(PTV_API_KEY, PTV_API_USERID), then the OS-native secret store (macOS
Keychain, Windows Credential Manager, Linux Secret Service), then an explicit
--env-file.

Use 'ptv auth login' to store PTV Timetable API credentials securely in the OS
secret store. Optional Transport Victoria Open Data credentials for GTFS
Realtime feeds are managed separately with 'ptv auth opendata'.`,
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Securely store PTV credentials in the OS secret store",
	Args:  cobra.NoArgs,
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
	Args:  cobra.NoArgs,
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
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			if !errors.Is(err, config.ErrMissingCredentials) {
				return err
			}
			if flagJSON {
				return printJSON(map[string]any{"configured": false})
			}
			fmt.Println("No credentials configured.")
			fmt.Println("Run 'ptv auth login' to store them securely.")
			return nil
		}
		if flagJSON {
			return printJSON(map[string]any{
				"configured": true,
				"source":     cfg.CredentialSource,
				"dev_id":     cfg.DevID,
			})
		}
		fmt.Printf("Credentials configured (source: %s)\n", render.CleanText(string(cfg.CredentialSource)))
		fmt.Printf("User/Developer ID: %s\n", render.CleanText(cfg.DevID))
		return nil
	},
}

var authCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify PTV API credentials with a signed request",
	Args:  cobra.NoArgs,
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
		fmt.Printf("Credentials OK (devid %s, source %s)\n", render.CleanText(cfg.DevID), render.CleanText(string(cfg.CredentialSource)))
		fmt.Printf("API version %s, health %d\n", render.CleanText(resp.Status.Version), resp.Status.Health)
		fmt.Printf("Available modes: ")
		for i, rt := range resp.RouteTypes {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Print(render.CleanText(rt.RouteTypeName))
		}
		fmt.Println()
		return nil
	},
}

var authOpenDataCmd = &cobra.Command{
	Use:   "opendata",
	Short: "Manage optional Transport Victoria Open Data credentials",
	Long: `Manage optional Transport Victoria Open Data credentials used for GTFS
Realtime trip updates, vehicle positions and service alerts.

Create an account at https://opendata.transport.vic.gov.au/ and subscribe to
the public transport data first. Store the subscription key with
'ptv auth opendata login'. If your account also requires a data platform API
token, enter it when prompted.`,
}

var authOpenDataLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Securely store Open Data credentials in the OS secret store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		keyID, err := promptSecret("Open Data subscription key (PTV_OPENDATA_KEY_ID): ")
		if err != nil {
			return err
		}
		apiID, err := promptSecret("Open Data API token (PTV_OPENDATA_API_ID, optional): ")
		if err != nil {
			return err
		}
		keyID = strings.TrimSpace(keyID)
		apiID = strings.TrimSpace(apiID)
		if keyID == "" {
			return fmt.Errorf("Open Data subscription key is required")
		}

		client := gtfsrt.New(keyID, apiID)
		feed, ok := gtfsrt.FeedByID("bus-vehicle-positions")
		if !ok {
			return fmt.Errorf("GTFS Realtime feed catalog is missing bus-vehicle-positions")
		}
		if _, err := client.Fetch(ctx(), feed.URL); err != nil {
			return fmt.Errorf("Open Data credentials rejected by GTFS Realtime API: %w", err)
		}

		if err := credstore.SaveOpenData(credstore.OpenDataCredentials{KeyID: keyID, APIID: apiID}); err != nil {
			return fmt.Errorf("storing Open Data credentials in OS keyring: %w", err)
		}
		fmt.Println("Open Data credentials verified and stored securely in the OS secret store.")
		return nil
	},
}

var authOpenDataStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether Open Data credentials are configured",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		creds, err := config.OpenDataCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv})
		if err != nil {
			return err
		}
		configured := creds.KeyID != ""
		if flagJSON {
			return printJSON(map[string]any{
				"configured": configured,
				"has_api_id": creds.APIID != "",
			})
		}
		if !configured {
			fmt.Println("No Open Data credentials configured.")
			fmt.Println("Run 'ptv auth opendata login' to store them securely.")
			return nil
		}
		fmt.Println("Open Data credentials configured.")
		if creds.APIID != "" {
			fmt.Println("Open Data API token configured.")
		}
		return nil
	},
}

var authOpenDataCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify Open Data credentials with a GTFS Realtime request",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		creds, err := config.OpenDataCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv})
		if err != nil {
			return err
		}
		if creds.KeyID == "" {
			return fmt.Errorf("missing Open Data credentials: run 'ptv auth opendata login' or set PTV_OPENDATA_KEY_ID")
		}
		feed, ok := gtfsrt.FeedByID("bus-vehicle-positions")
		if !ok {
			return fmt.Errorf("GTFS Realtime feed catalog is missing bus-vehicle-positions")
		}
		msg, err := gtfsrt.New(creds.KeyID, creds.APIID).Fetch(ctx(), feed.URL)
		if err != nil {
			return fmt.Errorf("Open Data credential check failed: %w", err)
		}
		if flagJSON {
			return printJSON(map[string]any{
				"ok":       true,
				"feed_id":  feed.ID,
				"entities": len(msg.GetEntity()),
			})
		}
		fmt.Printf("Open Data credentials OK (%s, %d entities)\n", render.CleanText(feed.Title), len(msg.GetEntity()))
		return nil
	},
}

var authOpenDataLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored Open Data credentials from the OS secret store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := credstore.DeleteOpenData(); err != nil {
			return fmt.Errorf("removing Open Data credentials: %w", err)
		}
		fmt.Println("Stored Open Data credentials removed from the OS secret store.")
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
	authOpenDataCmd.AddCommand(authOpenDataLoginCmd, authOpenDataLogoutCmd, authOpenDataStatusCmd, authOpenDataCheckCmd)
	authCmd.AddCommand(authLoginCmd, authLogoutCmd, authStatusCmd, authCheckCmd)
	authCmd.AddCommand(authOpenDataCmd)
	rootCmd.AddCommand(authCmd)
}
