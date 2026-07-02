package connector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iFixRobots/emaildawg/pkg/common"
	"github.com/iFixRobots/emaildawg/pkg/email"
	"github.com/iFixRobots/emaildawg/pkg/graph"
	"github.com/iFixRobots/emaildawg/pkg/imap"
	"github.com/iFixRobots/emaildawg/pkg/matrix"
	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// webhookItem is enqueued by handleGraphWebhookFull for background processing.
type webhookItem struct {
	messageID  string
	changeType string // e.g. "created", "updated", "deleted" — carried for Task 4
}

type EmailConnector struct {
	Bridge        *bridgev2.Bridge
	Config        Config
	IMAPManager   *imap.Manager
	RoomManager   *matrix.RoomManager
	ThreadManager *email.ThreadManager
	Processor     *email.Processor
	DB            *EmailAccountQuery

	// tokenProvider is set when OAuth2 (Microsoft 365 XOAUTH2) is enabled in
	// config. Shared across the manager and login flow.
	tokenProvider *imap.XOAuth2TokenProvider

	// Graph webhook fields — populated in Start() when a public_address is configured.
	graphClient      *graph.Client
	graphMu          sync.RWMutex // guards graphClientState
	graphClientState string       // expected clientState for signature validation; access via expectedClientState()
	webhookQueue     chan webhookItem

	// suppress prevents read-state feedback loops: when the bridge marks a
	// message read in Graph, it suppresses the resulting Graph webhook so we
	// don't re-deliver the read event back to Matrix. TTL = 45s.
	suppress *suppressCache
}

var (
	_ bridgev2.NetworkConnector              = (*EmailConnector)(nil)
	_ bridgev2.StoppableNetwork              = (*EmailConnector)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*EmailClient)(nil)
	_ bridgev2.TagHandlingNetworkAPI         = (*EmailClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*EmailClient)(nil)
)

// mergeProcessingConfig preserves YAML-parsed Processing values, defaulting
// only what the config file demonstrably lacks — Init used to clobber the
// whole section with defaults, which made reader_mode_extract: false a no-op.
// Bool-ambiguitet: config-filer fra før reader-nøglerne fandtes parser dem som
// false; når HELE reader-blokken er zero, er nøglerne fraværende → defaults.
// Eksplicit opt-out: sæt reader_mode: false SAMMEN med reader_mode_min_img_px.
func mergeProcessingConfig(parsed ProcessingConfig) ProcessingConfig {
	if parsed == (ProcessingConfig{}) {
		return defaultProcessingConfig()
	}
	if !parsed.ReaderMode && parsed.ReaderModeMinImgPx == 0 && !parsed.ReaderModeExtract {
		parsed.ReaderMode = true
		parsed.ReaderModeExtract = true
	}
	if parsed.ReaderModeMinImgPx <= 0 {
		parsed.ReaderModeMinImgPx = email.DefaultReaderModeMinImgPx
	}
	return parsed
}

// defaultProcessingConfig is the single source of ProcessingConfig defaults,
// shared by NewEmailConnector (tests) and Init (production) so they cannot drift.
func defaultProcessingConfig() ProcessingConfig {
	return ProcessingConfig{
		MaxUploadBytes:     DefaultMaxUploadBytes,
		GzipLargeBodies:    true,
		ReaderMode:         true,
		ReaderModeMinImgPx: email.DefaultReaderModeMinImgPx,
		ReaderModeExtract:  true,
	}
}

// NewEmailConnector returns an EmailConnector with default config pre-populated.
// It does not initialise a Bridge or any runtime dependencies; it is intended for
// testing and for callers that set up the Bridge separately via Init().
func NewEmailConnector() *EmailConnector {
	ec := &EmailConnector{}
	ec.Config = Config{
		Processing: defaultProcessingConfig(),
	}
	return ec
}

// outlookAvatar: Outlook-logo (pre-uploadet til hungryserv) som rum-avatar
// for alle email-tråde, så bridgede mails er visuelt genkendelige i Beeper.
const outlookRoomAvatarID = networkid.AvatarID("outlook-logo-v1")

var outlookAvatar = &bridgev2.Avatar{
	ID:  outlookRoomAvatarID,
	MXC: id.ContentURIString("mxc://local.beeper.com/stefanrosenlund_5TjQTmGOQ6Cjf9ZdXo0K6IqtWky6MTGGhRTvkQhBzEUOXEPrnaw5CECy561BN5tX"),
}

func (ec *EmailConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "EmailDawg",
		NetworkURL:           "https://en.wikipedia.org/wiki/Email",
		NetworkIcon:          "mxc://local.beeper.com/stefanrosenlund_5TjQTmGOQ6Cjf9ZdXo0K6IqtWky6MTGGhRTvkQhBzEUOXEPrnaw5CECy561BN5tX", // Outlook-logo (2018-2024)
		NetworkID:            "email",
		BeeperBridgeType:     "email",
		DefaultPort:          29319, // Different from WhatsApp's 29318
		DefaultCommandPrefix: "!email",
	}
}

func (ec *EmailConnector) Init(bridge *bridgev2.Bridge) {
	ec.Bridge = bridge

	// Initialize config with default values
	imapConfig := IMAPConfig{
		DefaultTimeout:            30,
		StartupBackfillSeconds:    180,
		StartupBackfillMax:        25,
		InitialIdleTimeoutSeconds: 3,
	}

	ec.Config = Config{
		IMAP: imapConfig,
		// Preserve any already-parsed config (oauth2, graph etc.) — Init must
		// not clobber them back to zero-values.
		OAuth2: ec.Config.OAuth2,
		Graph:  ec.Config.Graph,
		Network: NetworkConfig{
			IMAP: imapConfig, // Keep Network.IMAP populated for backward compatibility
		},
		Logging: LoggingConfig{
			Sanitized:       true,
			PseudonymSecret: "",
		},
		// Preserve parsed Processing (reader_mode_extract escape hatch etc.);
		// fall back to defaults only when nothing was parsed.
		Processing: mergeProcessingConfig(ec.Config.Processing),
	}

	// Allow environment overrides for verbose logging
	// EMAILDAWG_LOG_LEVEL: trace|debug|info|warn|error
	if lvl := strings.ToLower(os.Getenv("EMAILDAWG_LOG_LEVEL")); lvl != "" {
		switch lvl {
		case "trace":
			zerolog.SetGlobalLevel(zerolog.TraceLevel)
		case "debug":
			zerolog.SetGlobalLevel(zerolog.DebugLevel)
		case "info":
			zerolog.SetGlobalLevel(zerolog.InfoLevel)
		case "warn":
			zerolog.SetGlobalLevel(zerolog.WarnLevel)
		case "error":
			zerolog.SetGlobalLevel(zerolog.ErrorLevel)
		}
	} else {
		// Default to maximum verbosity for analysis
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	}
	if san := strings.ToLower(os.Getenv("EMAILDAWG_LOG_SANITIZED")); san == "false" || san == "0" || san == "no" {
		ec.Config.Logging.Sanitized = false
	}

	// Ensure ./data directory exists for local SQLite files and sidecar WAL/SHM files
	dataDir := filepath.Join(".", "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		bridge.Log.Warn().Err(err).Str("path", dataDir).Msg("Failed to ensure data directory exists")
	}
	if wd, err := os.Getwd(); err == nil {
		bridge.Log.Info().Str("cwd", wd).Str("data_dir", dataDir).Msg("Startup environment")
	}

	// Initialize database
	ec.DB = &EmailAccountQuery{
		DB: bridge.DB,
	}

	// Create database tables
	ctx := context.Background()
	if err := ec.DB.CreateTable(ctx); err != nil {
		bridge.Log.Error().Err(err).Msg("Failed to create email_accounts table - bridge initialization failed")
		panic(fmt.Errorf("database initialization failed: %w", err))
	}
	// Database health check: ensure we can write to the DB directory to avoid runtime I/O errors
	if err := ec.checkDBWritable(ctx); err != nil {
		bridge.Log.Error().Err(err).Msg("Database is not writable. Fix filesystem permissions or remove stale DB files, then restart the bridge.")
		panic(fmt.Errorf("database not writable: %w", err))
	}
	// Best-effort: add index for faster message lookups by (network, remote_id) if schema matches.
	if _, err := bridge.DB.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_message_network_remote ON message(network, remote_id)`); err == nil {
		bridge.Log.Trace().Msg("Ensured index idx_message_network_remote on message(network, remote_id)")
	}
	if _, err := bridge.DB.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_messages_network_remote ON messages(network, remote_id)`); err == nil {
		bridge.Log.Trace().Msg("Ensured index idx_messages_network_remote on messages(network, remote_id)")
	}

	// Initialize the suppression cache for Graph read-state loop prevention.
	ec.suppress = newSuppressCache()

	// Initialize managers
	logger := bridge.Log.With().Str("component", "imap").Logger()
	if ec.Config.OAuth2.Enabled {
		ec.tokenProvider = imap.NewXOAuth2TokenProvider(ec.Config.OAuth2.TenantID, ec.Config.OAuth2.ClientID, ec.Config.OAuth2.ClientSecret)
		logger.Info().Msg("XOAUTH2 (Microsoft 365) authentication enabled")
	}
	ec.IMAPManager = imap.NewManager(bridge, &logger, ec.Config.Logging.Sanitized, ec.Config.Logging.PseudonymSecret, ec.tokenProvider)

	roomLogger := bridge.Log.With().Str("component", "matrix").Logger()
	ec.RoomManager = matrix.NewRoomManager(&roomLogger)

	// Prefer a DB-backed resolver that can find existing portals by prior bridged messages
	resolver := &DBThreadMetadataResolver{Bridge: bridge, Log: &roomLogger, Network: "email"}
	ec.ThreadManager = email.NewThreadManager(resolver)

	// Initialize email processor and wire it to the IMAP manager
	processorLogger := bridge.Log.With().Str("component", "email_processor").Logger()
	ec.Processor = email.NewProcessor(&processorLogger, ec.ThreadManager, ec.Config.Logging.Sanitized, ec.Config.Logging.PseudonymSecret)
	// Apply processing config
	if ec.Config.Processing.MaxUploadBytes > 0 {
		ec.Processor.MaxUploadBytes = ec.Config.Processing.MaxUploadBytes
	}
	ec.Processor.GzipLargeBodies = ec.Config.Processing.GzipLargeBodies
	ec.Processor.ReaderMode = ec.Config.Processing.ReaderMode
	ec.Processor.ReaderModeMinImgPx = ec.Config.Processing.ReaderModeMinImgPx
	ec.Processor.ReaderModeExtract = ec.Config.Processing.ReaderModeExtract
	if ec.Processor.ReaderModeMinImgPx <= 0 {
		ec.Processor.ReaderModeMinImgPx = email.DefaultReaderModeMinImgPx
	}
	ec.IMAPManager.SetProcessor(ec.Processor)

	// Add commands with connector context
	ec.Bridge.Commands.(*commands.Processor).AddHandlers(
		ec.createCommands()...,
	)
}

// createCommands creates command handlers with access to this connector instance
func (ec *EmailConnector) createCommands() []commands.CommandHandler {
	return []commands.CommandHandler{
		&commands.FullHandler{
			Func: fnPing,
			Name: "ping",
			Help: commands.HelpMeta{
				Section:     HelpSectionInfo,
				Description: "Check if the bridge is alive",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnStatus(ce, ec) },
			Name: "status",
			Help: commands.HelpMeta{
				Section:     HelpSectionInfo,
				Description: "Show connection status for your email accounts",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnLogin(ce, ec) },
			Name: "login",
			Help: commands.HelpMeta{
				Section:     HelpSectionAuth,
				Description: "Login to an email account",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnLogout(ce, ec) },
			Name: "logout",
			Help: commands.HelpMeta{
				Section:     HelpSectionAuth,
				Description: "Logout from email accounts",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnList(ce, ec) },
			Name: "list",
			Help: commands.HelpMeta{
				Section:     HelpSectionInfo,
				Description: "List configured email accounts",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnSync(ce, ec) },
			Name: "sync",
			Help: commands.HelpMeta{
				Section:     HelpSectionInfo,
				Description: "Manually trigger email synchronization",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnReconnect(ce, ec) },
			Name: "reconnect",
			Help: commands.HelpMeta{
				Section:     HelpSectionAdmin,
				Description: "Reconnect to email servers",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnNuke(ce, ec) },
			Name: "nuke",
			Help: commands.HelpMeta{
				Section:     HelpSectionAdmin,
				Description: "Remove all email accounts and reset database",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnPassphrase(ce, ec) },
			Name: "passphrase",
			Help: commands.HelpMeta{
				Section:     HelpSectionAdmin,
				Description: "Show database encryption passphrase",
			},
		},
		&commands.FullHandler{
			Func: func(ce *commands.Event) { fnConfig(ce, ec) },
			Name: "config",
			Help: commands.HelpMeta{
				Section:     HelpSectionAdmin,
				Description: "Configure email bridge settings (e.g., config folders)",
			},
		},
	}
}

func (ec *EmailConnector) Start(ctx context.Context) error {
	ec.Bridge.Log.Info().Msg("Email connector starting...")
	if err := ec.autoLogin(ctx); err != nil {
		// Non-fatal: the bridge keeps running so the account can still be added
		// manually via the bot if auto-login hits a problem.
		ec.Bridge.Log.Error().Err(err).Msg("XOAUTH2 auto-login failed")
	}

	// Only start the Graph webhook infrastructure when Graph mode is enabled and
	// a public address is configured (needed so ensureGraphSubscription can build
	// the correct notificationUrl). The dedicated HTTP server on :29319 is used
	// instead of the appservice mux so it works in Beeper bbctl websocket-mode
	// where no inbound HTTP listener is started by the appservice.
	if !ec.Config.Graph.Enabled {
		return nil
	}

	// A public_address is still required so ensureGraphSubscription can build the
	// correct notificationUrl for the Microsoft Graph subscription.
	matrixWithServer, ok := ec.Bridge.Matrix.(interface{ GetPublicAddress() string })
	if !ok || matrixWithServer.GetPublicAddress() == "" {
		ec.Bridge.Log.Warn().Msg("Graph mode enabled but no public_address configured; Graph webhooks disabled")
		return nil
	}

	// Build a Graph client when OAuth2 credentials are available.
	if ec.Config.OAuth2.Enabled && ec.Config.OAuth2.TenantID != "" {
		tp := graph.NewTokenProvider(
			ec.Config.OAuth2.TenantID,
			ec.Config.OAuth2.ClientID,
			ec.Config.OAuth2.ClientSecret,
		)
		ec.graphClient = graph.NewClient(tp, ec.Config.OAuth2.AutoLoginEmail)
	}

	// graphClientState will be set by ensureGraphSubscription to the randomly
	// generated clientState persisted in the graph_state table. Initialise to
	// an empty sentinel here — the webhook handler will discard anything that
	// does not match the value set by ensureGraphSubscription.
	ec.graphMu.Lock()
	ec.graphClientState = ""
	ec.graphMu.Unlock()

	// Buffered channel to decouple the HTTP handler (must respond in 3s) from
	// the blocking GetMessage + deliver work.
	ec.webhookQueue = make(chan webhookItem, 256)

	// Background worker: drain the queue, fetch each message, deliver.
	go ec.runWebhookWorker(ctx)

	// Start the dedicated webhook HTTP server. It must be up before we call
	// ensureGraphSubscription so that Graph can reach the endpoint during the
	// subscription-validation handshake.
	if err := ec.startGraphWebhookServer(ctx); err != nil {
		ec.Bridge.Log.Warn().Err(err).Msg("Graph webhook server failed to start; skipping Graph subscription")
		return nil
	}

	// Create or reuse the Graph subscription (random clientState, persisted,
	// renewal goroutine). This is a no-op when OAuth2 is not configured.
	ec.ensureGraphSubscription(ctx)

	// One-shot stuck-message recovery (EMAILDAWG_FORCE_REDELIVER). No-op unless set.
	go ec.forceRedeliver(ctx)

	// Periodic self-heal: re-deliver orphaned messages whose Matrix event is
	// missing (disable with EMAILDAWG_SELFHEAL=off).
	go ec.selfHealLoop(ctx)

	// One-shot clean inbox rebuild (EMAILDAWG_REBUILD_INBOX=1). No-op unless set.
	go ec.forceRebuildInbox(ctx)

	// One-shot attachment backfill (EMAILDAWG_BACKFILL_ATTACHMENTS=1): re-delivers
	// bilag for mails bridged before attachment support, as separate bilag-messages.
	// No-op unless set.
	go ec.backfillAttachments(ctx)

	// Periodically ensure the user is joined to all email rooms — live webhook
	// rooms aren't reliably auto-joined on Beeper (disable with EMAILDAWG_JOINHEAL=off).
	go ec.ensureJoinedLoop(ctx)

	// Periodic incremental delta reconcile — catches messages that left the inbox
	// (Outlook archive/delete → Flow 5/7) which the webhook path doesn't surface.
	go ec.reconcileLoop(ctx)

	return nil
}

// runWebhookWorker drains the webhook queue and delivers each message.
func (ec *EmailConnector) runWebhookWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item, ok := <-ec.webhookQueue:
			if !ok {
				return
			}
			ec.processWebhookItem(ctx, item)
		}
	}
}

// processWebhookItem fetches a Graph message and delivers it to Matrix.
// Full multi-account routing (matching UserLogin by email) is deferred to a
// later task. Here we fetch the message and then attempt delivery via the auto-
// login account's EmailClient if it is already wired into the bridge.
//
// changeType routing:
//   - "created" → deliver the full message (existing path).
//   - "updated" → fetch message; if IsRead and NOT suppressed (echo of our own
//     PATCH from Task 2), queue a read receipt. Suppression is keyed on
//     InternetMessageID (same key used in Task 2's MarkRead suppress call).
func (ec *EmailConnector) processWebhookItem(ctx context.Context, item webhookItem) {
	if ec.graphClient == nil {
		ec.Bridge.Log.Warn().Str("message_id", item.messageID).Msg("Graph webhook: no graphClient configured, cannot fetch message")
		return
	}

	msg, err := ec.graphClient.GetMessage(ctx, item.messageID)
	if err != nil {
		ec.Bridge.Log.Error().Err(err).Str("message_id", item.messageID).Msg("Graph webhook: GetMessage failed")
		return
	}

	// Locate the UserLogin for the auto-login mailbox and deliver via the first
	// matching EmailClient. Full multi-account routing comes in Task 4.
	// Format must stay in sync with autoLogin(), which uses the same "email:<addr>" pattern.
	loginID := networkid.UserLoginID(fmt.Sprintf("email:%s", ec.Config.OAuth2.AutoLoginEmail))
	login := ec.Bridge.GetCachedUserLoginByID(loginID)
	if login == nil {
		ec.Bridge.Log.Warn().Str("message_id", item.messageID).Msg("Graph webhook: auto-login UserLogin not found, cannot deliver")
		return
	}
	client, ok := login.Client.(*EmailClient)
	if !ok {
		ec.Bridge.Log.Warn().Str("message_id", item.messageID).Msg("Graph webhook: UserLogin client is not an EmailClient")
		return
	}

	switch item.changeType {
	case "updated":
		// Suppress if this "updated" notification is the echo of our own
		// PATCH (Task 2 marks the message read in Graph; Graph then fires an
		// "updated" webhook back to us). Suppression key = InternetMessageID.
		if ec.suppress.IsSuppressed(msg.InternetMessageID) {
			ec.Bridge.Log.Debug().
				Str("internet_message_id", msg.InternetMessageID).
				Msg("Graph webhook: suppressed updated notification (echo of own PATCH)")
			return
		}
		if msg.IsRead {
			client.deliverReadReceipt(ctx, msg)
		} else {
			ec.Bridge.Log.Debug().
				Str("internet_message_id", msg.InternetMessageID).
				Msg("Graph webhook: updated event with isRead=false, ignoring")
		}
	default:
		// "created" (and anything else) → existing full-message deliver path.
		client.deliverGraphMessage(ctx, msg)
	}
}

// autoLogin logs the configured mailbox in at startup via XOAUTH2 when
// oauth2.auto_login_email + owner_mxid are set, so no interactive bot login is
// needed. Idempotent: it does nothing if the account already exists in the DB.
func (ec *EmailConnector) autoLogin(ctx context.Context) error {
	cfg := ec.Config.OAuth2
	ec.Bridge.Log.Info().
		Bool("enabled", cfg.Enabled).
		Bool("has_tenant", cfg.TenantID != "").
		Str("auto_login_email", cfg.AutoLoginEmail).
		Str("owner_mxid", cfg.OwnerMXID).
		Msg("autoLogin: config check")
	if !cfg.Enabled || cfg.AutoLoginEmail == "" || cfg.OwnerMXID == "" {
		return nil
	}
	log := ec.Bridge.Log.With().Str("component", "auto_login").Str("email", cfg.AutoLoginEmail).Logger()

	// Idempotency: skip if the account already exists (the framework loads
	// existing logins on its own, regardless of Start() ordering).
	existing, err := ec.DB.GetAccount(ctx, cfg.OwnerMXID, cfg.AutoLoginEmail)
	if err != nil {
		return fmt.Errorf("check existing account: %w", err)
	}
	if existing != nil {
		log.Info().Msg("Auto-login: account already configured, skipping")
		return nil
	}

	owner, err := ec.Bridge.GetUserByMXID(ctx, id.UserID(cfg.OwnerMXID))
	if err != nil {
		return fmt.Errorf("get owner user %q: %w", cfg.OwnerMXID, err)
	}

	// When the Graph read-path is active, skip IMAP AddAccount / IDLE polling.
	// The Graph webhook + delta backfill (wired in Start()) handle mail delivery
	// instead. We still need the bridgev2 UserLogin to exist so that
	// processWebhookItem can look it up via GetCachedUserLoginByID.
	if ec.Config.Graph.Enabled {
		log.Info().Msg("Auto-login: graph.enabled=true — skipping IMAP AddAccount; Graph read-path handles delivery")
		loginID := networkid.UserLoginID(fmt.Sprintf("email:%s", cfg.AutoLoginEmail))
		_, err = owner.NewLogin(ctx, &database.UserLogin{
			ID:         loginID,
			RemoteName: cfg.AutoLoginEmail,
			Metadata:   &EmailLoginMetadata{Email: cfg.AutoLoginEmail, Username: cfg.AutoLoginEmail},
		}, &bridgev2.NewLoginParams{DeleteOnConflict: false})
		if err != nil {
			return fmt.Errorf("create login (graph mode): %w", err)
		}
		log.Info().Msg("Auto-login: UserLogin created for Graph read-path")
		return nil
	}

	folders := cfg.AutoLoginFolders
	if len(folders) == 0 {
		folders = []string{"INBOX"}
	}

	// Store the account. Password is empty: XOAUTH2 uses the app-only token, so
	// the IMAP client ignores it (host is forced to Office 365 in OAuth2 mode).
	account := &EmailAccount{
		UserMXID:         cfg.OwnerMXID,
		Email:            cfg.AutoLoginEmail,
		Username:         cfg.AutoLoginEmail,
		Password:         "",
		Host:             "outlook.office365.com",
		Port:             993,
		TLS:              true,
		CreatedAt:        time.Now(),
		LastSyncTime:     time.Now(),
		MonitoredFolders: folders,
	}
	if err := ec.DB.UpsertAccount(ctx, account); err != nil {
		return fmt.Errorf("save account: %w", err)
	}

	// Create the UserLogin — this triggers LoadUserLogin, which starts the IMAP
	// (XOAUTH2) connection.
	loginID := networkid.UserLoginID(fmt.Sprintf("email:%s", cfg.AutoLoginEmail))
	_, err = owner.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: cfg.AutoLoginEmail,
		Metadata:   &EmailLoginMetadata{Email: cfg.AutoLoginEmail, Username: cfg.AutoLoginEmail},
	}, &bridgev2.NewLoginParams{DeleteOnConflict: false})
	if err != nil {
		return fmt.Errorf("create login: %w", err)
	}

	log.Info().Strs("folders", folders).Msg("Auto-login: mailbox configured via XOAUTH2")
	return nil
}

// Stop gracefully shuts down the EmailConnector and all IMAP connections
func (ec *EmailConnector) Stop() {
	ec.Bridge.Log.Info().Msg("Email connector stopping...")

	// Stop all IMAP clients
	if ec.IMAPManager != nil {
		ec.IMAPManager.StopAll()
	}

	ec.Bridge.Log.Info().Msg("Email connector stopped")
}

// LoadUserLogin is now implemented in client.go

// expectedClientState returns the current graphClientState value safely under a read lock.
func (ec *EmailConnector) expectedClientState() string {
	ec.graphMu.RLock()
	defer ec.graphMu.RUnlock()
	return ec.graphClientState
}

func (ec *EmailConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		DisappearingMessages: false,
		AggressiveUpdateInfo: false,
	}
}

func (ec *EmailConnector) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	// Extract email address from ghost ID
	userIDStr := string(ghost.ID)
	// Remove "email:" prefix if present
	if len(userIDStr) > 6 && userIDStr[:6] == "email:" {
		userIDStr = userIDStr[6:]
	}
	return &bridgev2.UserInfo{
		Name:        &userIDStr,
		Avatar:      nil,
		IsBot:       nil,
		Identifiers: []string{userIDStr},
	}, nil
}

// GetChatInfo implements the bridgev2 interface for portal/room creation
func (ec *EmailConnector) GetChatInfo(ctx context.Context, portal *bridgev2.Portal, userLogin *bridgev2.UserLogin, portalID networkid.PortalKey) (*bridgev2.ChatInfo, error) {
	// Extract thread ID from portal ID
	threadID := string(portalID.ID)
	if len(threadID) > 7 && threadID[:7] == "thread:" {
		threadID = threadID[7:] // Remove "thread:" prefix
	}

	// If we have richer thread info, build the room using RoomManager
	if ec.ThreadManager != nil && ec.RoomManager != nil {
		if thread := ec.ThreadManager.GetThreadByID(string(userLogin.ID), threadID); thread != nil {
			return ec.RoomManager.GetChatInfoForThread(ctx, thread, userLogin)
		}
	}

	// Fallback: basic room configuration when thread is not yet known
	roomName := fmt.Sprintf("Email Thread: %s", threadID)
	roomTopic := "Email thread - messages will appear here when emails are received"

	// Include the Matrix user as an auto-joined member (IsFromMe) so the room is
	// auto-joined at creation (Beeper BeeperInitialMembers) instead of leaving the
	// user uninvited. An uninvited user sits at power level 0 and cannot self-join,
	// so the later membership=join PUT 403s and the room never surfaces in Beeper.
	// Mirrors RoomManager.GetChatInfoForThread (the path that already works).
	selfMember := bridgev2.ChatMember{
		EventSender: bridgev2.EventSender{IsFromMe: true},
		Membership:  event.MembershipJoin,
		// PowerLevel intentionally omitted (defaults to 0) to keep the room read-only.
	}

	chatInfo := &bridgev2.ChatInfo{
		Name:   &roomName,
		Topic:  &roomTopic,
		Avatar: outlookAvatar,
		Type:   ptr.Ptr(database.RoomTypeDefault),
		Members: &bridgev2.ChatMemberList{
			IsFull:           true,
			TotalMemberCount: 1,
			Members:          []bridgev2.ChatMember{selfMember},
			MemberMap:        map[networkid.UserID]bridgev2.ChatMember{networkid.UserID(""): selfMember},
		},
	}

	ec.Bridge.Log.Debug().
		Str("thread_id", threadID).
		Str("room_name", roomName).
		Msg("Created fallback ChatInfo for email thread")

	return chatInfo, nil
}

// Required methods for NetworkConnector interface
func (ec *EmailConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	return &EmailLoginProcess{user: user, connector: ec}, nil
}

func (ec *EmailConnector) GetDBMetaTables() []any {
	return []any{
		&EmailAccount{},
	}
}

func (ec *EmailConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{}
}

func (ec *EmailConnector) GetBridgeInfoVersion() (int, int) {
	// Info-version bumpet til 2: NetworkIcon skiftet til Outlook-logo — trigger
	// resend af bridge-info til alle eksisterende portaler ved opstart.
	return 2, 1
}

func (ec *EmailConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "email-password",
		Description: "Email and password login",
		ID:          "email-password",
	}}
}

// Helper functions for creating network IDs
func MakeUserID(email string) networkid.UserID {
	return common.EmailToGhostID(email)
}

func MakePortalID(threadID string) networkid.PortalID {
	return networkid.PortalID(fmt.Sprintf("thread:%s", threadID))
}

// checkDBWritable attempts a few write operations to ensure the underlying DB is writable
// and the directory allows journaling/WAL files. This prevents silent runtime failures later.
func (ec *EmailConnector) checkDBWritable(ctx context.Context) error {
	// Create a tiny health table and write a row, then delete it.
	_, err := ec.Bridge.DB.Exec(ctx, `CREATE TABLE IF NOT EXISTS email_health_check (ts INTEGER NOT NULL)`)
	if err != nil {
		return fmt.Errorf("failed to create health check table: %w", err)
	}
	_, err = ec.Bridge.DB.Exec(ctx, `INSERT INTO email_health_check (ts) VALUES (?)`, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("failed to insert into health check table: %w", err)
	}
	_, err = ec.Bridge.DB.Exec(ctx, `DELETE FROM email_health_check`)
	if err != nil {
		return fmt.Errorf("failed to delete from health check table: %w", err)
	}
	return nil
}
