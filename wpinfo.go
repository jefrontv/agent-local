package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// wpInfoPHP is run through `wp eval` (plugins and themes loaded normally, so
// get_plugins()/wp_get_theme() see the real install) and echoes a single
// JSON object describing the site from WordPress's own point of view. It is
// wrapped in try/catch so a fatal in a plugin's init hook still yields a
// diagnosable {"error": ...} instead of a bare wp-cli crash.
const wpInfoPHP = `try {
	$plugins = array();
	$active = (array) get_option( 'active_plugins', array() );
	if ( is_multisite() ) {
		$active = array_merge( $active, array_keys( (array) get_site_option( 'active_sitewide_plugins', array() ) ) );
	}
	$updates = get_site_transient( 'update_plugins' );
	$updatable = array();
	if ( $updates && ! empty( $updates->response ) ) {
		$updatable = array_keys( (array) $updates->response );
	}
	foreach ( get_plugins() as $file => $data ) {
		$plugins[] = array(
			'name' => $data['Name'],
			'file' => $file,
			'version' => $data['Version'],
			'active' => in_array( $file, $active, true ),
			'update_available' => in_array( $file, $updatable, true ),
		);
	}
	$theme = wp_get_theme();
	$parent = $theme->parent();
	$admins = array();
	foreach ( get_users( array( 'role' => 'administrator', 'fields' => array( 'user_login', 'user_email' ), 'number' => 20 ) ) as $u ) {
		$admins[] = array( 'login' => $u->user_login, 'email' => $u->user_email );
	}
	$cron_next = null;
	$cron = _get_cron_array();
	if ( is_array( $cron ) && ! empty( $cron ) ) {
		$cron_next = (int) min( array_keys( $cron ) );
	}
	$uploads = wp_upload_dir();
	$out = array(
		'wp_version' => get_bloginfo( 'version' ),
		'php_version' => PHP_VERSION,
		'home' => home_url(),
		'siteurl' => site_url(),
		'multisite' => is_multisite(),
		'permalink_structure' => get_option( 'permalink_structure' ),
		'table_prefix' => $GLOBALS['table_prefix'],
		'theme' => array(
			'name' => $theme->get( 'Name' ),
			'version' => $theme->get( 'Version' ),
			'stylesheet' => $theme->get_stylesheet(),
			'template' => $theme->get_template(),
			'parent' => $parent ? $parent->get( 'Name' ) : null,
		),
		'plugins' => $plugins,
		'dropins' => array_keys( get_dropins() ),
		'debug' => array(
			'wp_debug' => defined( 'WP_DEBUG' ) && WP_DEBUG,
			'wp_debug_log' => defined( 'WP_DEBUG_LOG' ) ? WP_DEBUG_LOG : false,
			'wp_debug_display' => defined( 'WP_DEBUG_DISPLAY' ) ? WP_DEBUG_DISPLAY : false,
			'script_debug' => defined( 'SCRIPT_DEBUG' ) && SCRIPT_DEBUG,
		),
		'cron' => array(
			'disabled' => defined( 'DISABLE_WP_CRON' ) && DISABLE_WP_CRON,
			'next' => $cron_next,
		),
		'admins' => $admins,
		'uploads' => array(
			'basedir' => $uploads['basedir'],
			'baseurl' => $uploads['baseurl'],
		),
		'object_cache' => wp_using_ext_object_cache(),
		'environment_type' => wp_get_environment_type(),
	);
	echo json_encode( $out );
} catch ( \Throwable $e ) {
	echo json_encode( array( 'error' => $e->getMessage() ) );
}`

// wpInfoTheme is wpInfo.theme.
type wpInfoTheme struct {
	Name       string  `json:"name"`
	Version    string  `json:"version"`
	Stylesheet string  `json:"stylesheet"`
	Template   string  `json:"template"`
	Parent     *string `json:"parent"`
}

// wpInfoPlugin is one entry of wpInfo.plugins.
type wpInfoPlugin struct {
	Name            string `json:"name"`
	File            string `json:"file"`
	Version         string `json:"version"`
	Active          bool   `json:"active"`
	UpdateAvailable bool   `json:"update_available"`
}

// wpInfoDebug is wpInfo.debug. WP_DEBUG_LOG is the odd one: WordPress accepts
// true (log beside wp-content) or a path, so it is carried as whatever PHP
// had - false, true, or the path string.
type wpInfoDebug struct {
	WPDebug        bool        `json:"wp_debug"`
	WPDebugLog     interface{} `json:"wp_debug_log"`
	WPDebugDisplay bool        `json:"wp_debug_display"`
	ScriptDebug    bool        `json:"script_debug"`
}

// wpInfoCron is wpInfo.cron.
type wpInfoCron struct {
	Disabled bool   `json:"disabled"`
	Next     *int64 `json:"next"`
}

// wpInfoAdmin is one entry of wpInfo.admins.
type wpInfoAdmin struct {
	Login string `json:"login"`
	Email string `json:"email"`
}

// wpInfoUploads is wpInfo.uploads.
type wpInfoUploads struct {
	BaseDir string `json:"basedir"`
	BaseURL string `json:"baseurl"`
}

// wpInfo is what WordPress itself reports about a site, plus agent-local's
// own read of whether the site the daemon is serving matches what WordPress
// thinks its home URL is. The two can disagree - a stale wp_options row, an
// import that didn't rewrite URLs - and that mismatch is usually the actual
// bug an agent is chasing.
type wpInfo struct {
	Error              string         `json:"error,omitempty"`
	WPVersion          string         `json:"wp_version"`
	PHPVersion         string         `json:"php_version"`
	Home               string         `json:"home"`
	SiteURL            string         `json:"siteurl"`
	Multisite          bool           `json:"multisite"`
	PermalinkStructure string         `json:"permalink_structure"`
	TablePrefix        string         `json:"table_prefix"`
	Theme              wpInfoTheme    `json:"theme"`
	Plugins            []wpInfoPlugin `json:"plugins"`
	Dropins            []string       `json:"dropins"`
	Debug              wpInfoDebug    `json:"debug"`
	Cron               wpInfoCron     `json:"cron"`
	Admins             []wpInfoAdmin  `json:"admins"`
	Uploads            wpInfoUploads  `json:"uploads"`
	ObjectCache        bool           `json:"object_cache"`
	EnvironmentType    string         `json:"environment_type"`

	// agent-local's own additions: none of this comes from the PHP snippet.
	ServedDomain        string   `json:"served_domain"`
	ServedDomainMatches bool     `json:"served_domain_matches"`
	Pins                []string `json:"pins,omitempty"`
}

// extractJSONObject pulls the outermost {...} out of wp-cli output. wp-cli
// and misbehaving plugins are free to print notices before the eval's own
// echo runs; only the JSON itself is wanted.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// finishWPInfo fills in the fields wpInfo carries that WordPress itself
// never reports: whether the domain this daemon serves the site on is one
// WordPress would also answer to, and the URL constants pinned in
// wp-config.php. Factored out of WPInfo so it can be exercised directly
// without shelling out to wp-cli.
func finishWPInfo(info *wpInfo, site *Site) {
	info.ServedDomain = site.Domain
	info.ServedDomainMatches = site.ownsHost(hostFromURL(info.Home))
	var pins []string
	for _, p := range wpConfigURLPins(site.WPDir) {
		pins = append(pins, p.Name+"="+p.URL)
	}
	info.Pins = pins
}

// WPInfo asks WordPress, from inside its own bootstrap, what it thinks it
// is: version, active theme/plugins, debug flags, admins, cron state. This
// is the install's self-report, distinct from ProbeSite's outside-in view
// of what a browser actually receives.
func (e *Engine) WPInfo(site *Site) (*wpInfo, error) {
	out, err := wpCLI(site, "eval", wpInfoPHP)
	body := extractJSONObject(out)
	if err != nil || body == "" {
		tail := out
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return nil, fmt.Errorf("wordpress could not load: %s — run probe_site for the request-level picture", tail)
	}
	var info wpInfo
	if decErr := json.Unmarshal([]byte(body), &info); decErr != nil {
		tail := out
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return nil, fmt.Errorf("wordpress could not load: %s — run probe_site for the request-level picture", tail)
	}
	if info.Error != "" {
		return nil, fmt.Errorf("wordpress could not load: %s — run probe_site for the request-level picture", info.Error)
	}
	finishWPInfo(&info, site)
	return &info, nil
}

// handleWPInfo serves GET /sites/{slug}/wp-info: WordPress's self-report for
// agents that need to know what's actually installed and active, not just
// whether the site answers HTTP requests.
func (a *APIServer) handleWPInfo(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	info, err := a.engine.WPInfo(site)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	ok(w, info)
}
