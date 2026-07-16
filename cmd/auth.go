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
		runtimeCfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		client := ptvapi.New(runtimeCfg.BaseURL, apiKey, devID)
		if _, err := client.RouteTypes(cmd.Context()); err != nil {
			return fmt.Errorf("credentials rejected by PTV API: %w", err)
		}

		if err := credstore.Save(credstore.Credentials{APIKey: apiKey, DevID: devID}); err != nil {
			return fmt.Errorf("storing credentials in OS keyring: %w", err)
		}
		if flagJSON {
			return printJSON(map[string]any{"credential": "ptv_timetable", "verified": true, "stored": true})
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
		if flagJSON {
			return printJSON(map[string]any{"credential": "ptv_timetable", "removed": true})
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
		credentials, err := config.LoadPTVCredentialsWithOptions(config.LoadOptions{EnvFile: flagEnv})
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
				"source":     credentials.Source,
				"dev_id":     credentials.DevID,
			})
		}
		fmt.Printf("Credentials configured (source: %s)\n", render.CleanText(string(credentials.Source)))
		fmt.Printf("User/Developer ID: %s\n", render.CleanText(credentials.DevID))
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
		resp, err := client.RouteTypes(cmd.Context())
		if err != nil {
			return fmt.Errorf("credential check failed: %w", err)
		}
		if flagJSON {
			return printJSON(newAuthCheckOutput(resp))
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

type authCheckOutput struct {
	RouteTypes []authRouteTypeOutput `json:"route_types"`
	Status     authStatusOutput      `json:"status"`
}

type authRouteTypeOutput struct {
	RouteTypeName string `json:"route_type_name"`
	RouteType     int    `json:"route_type"`
}

type authStatusOutput struct {
	Version string `json:"version"`
	Health  int    `json:"health"`
}

func newAuthCheckOutput(response *ptvapi.RouteTypesResponse) authCheckOutput {
	output := authCheckOutput{
		RouteTypes: make([]authRouteTypeOutput, 0, len(response.RouteTypes)),
		Status: authStatusOutput{
			Version: normalizedText(response.Status.Version),
			Health:  response.Status.Health,
		},
	}
	for _, routeType := range response.RouteTypes {
		output.RouteTypes = append(output.RouteTypes, authRouteTypeOutput{
			RouteTypeName: normalizedText(routeType.RouteTypeName),
			RouteType:     routeType.RouteType,
		})
	}
	return output
}

var authOpenDataCmd = &cobra.Command{
	Use:   "opendata",
	Short: "Manage optional Transport Victoria Open Data credentials",
	Long: `Manage optional Transport Victoria Open Data credentials used for GTFS
Realtime trip updates, vehicle positions and service alerts.

Create an account at https://opendata.transport.vic.gov.au/ and subscribe to
the public transport data first. Store the subscription key with
'ptv auth opendata login'. A previously stored PTV_OPENDATA_API_ID remains
readable for compatibility but is not transmitted because the current GTFS
Realtime feed contract documents only the KeyID subscription header.`,
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
		apiID, err := promptSecret("Legacy Open Data API token (stored but not transmitted, optional): ")
		if err != nil {
			return err
		}
		keyID = strings.TrimSpace(keyID)
		apiID = strings.TrimSpace(apiID)
		if keyID == "" {
			return fmt.Errorf("Open Data subscription key is required")
		}

		client := gtfsrt.NewWithOptions(keyID, gtfsrt.ClientOptions{})
		feed, ok := gtfsrt.FeedByID("bus-vehicle-positions")
		if !ok {
			return fmt.Errorf("GTFS Realtime feed catalog is missing bus-vehicle-positions")
		}
		if _, err := client.FetchSnapshot(cmd.Context(), feed); err != nil {
			return fmt.Errorf("Open Data credentials rejected by GTFS Realtime API: %w", err)
		}

		if err := credstore.SaveOpenData(credstore.OpenDataCredentials{KeyID: keyID, APIID: apiID}); err != nil {
			return fmt.Errorf("storing Open Data credentials in OS keyring: %w", err)
		}
		if flagJSON {
			return printJSON(map[string]any{
				"credential": "transport_victoria_open_data", "verified": true, "stored": true,
				"api_id_transmitted": false,
			})
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
				"configured":         configured,
				"has_api_id":         creds.APIID != "",
				"api_id_transmitted": false,
			})
		}
		if !configured {
			fmt.Println("No Open Data credentials configured.")
			fmt.Println("Run 'ptv auth opendata login' to store them securely.")
			return nil
		}
		fmt.Println("Open Data credentials configured.")
		if creds.APIID != "" {
			fmt.Println("Legacy Open Data API token configured (retained but not transmitted).")
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
		snapshot, err := gtfsrt.NewWithOptions(creds.KeyID, gtfsrt.ClientOptions{}).FetchSnapshot(cmd.Context(), feed)
		if err != nil {
			return fmt.Errorf("Open Data credential check failed: %w", err)
		}
		if flagJSON {
			return printJSON(map[string]any{
				"ok":                    true,
				"feed_id":               feed.ID,
				"entities":              snapshot.Counts.Entities,
				"authentication_header": gtfsrt.KeyIDHeader,
				"api_id_transmitted":    false,
			})
		}
		fmt.Printf("Open Data credentials OK (%s, %d entities)\n", render.CleanText(feed.Title), snapshot.Counts.Entities)
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
		if flagJSON {
			return printJSON(map[string]any{"credential": "transport_victoria_open_data", "removed": true})
		}
		fmt.Println("Stored Open Data credentials removed from the OS secret store.")
		return nil
	},
}

// stdinReader is shared across prompts so buffered reads don't discard input.
var stdinReader = bufio.NewReader(os.Stdin)

// promptLine prints a prompt and reads a line from stdin.
func promptLine(prompt string) (string, error) {
	fmt.Fprint(promptOutput(), prompt)
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// promptSecret reads a secret without echoing it when stdin is a terminal.
func promptSecret(prompt string) (string, error) {
	fmt.Fprint(promptOutput(), prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(promptOutput())
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return promptLine("")
}

func promptOutput() *os.File {
	if flagJSON {
		return os.Stderr
	}
	return os.Stdout
}

func init() {
	authOpenDataCmd.AddCommand(authOpenDataLoginCmd, authOpenDataLogoutCmd, authOpenDataStatusCmd, authOpenDataCheckCmd)
	authCmd.AddCommand(authLoginCmd, authLogoutCmd, authStatusCmd, authCheckCmd)
	authCmd.AddCommand(authOpenDataCmd)
	rootCmd.AddCommand(authCmd)
}
