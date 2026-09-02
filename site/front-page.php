<?php
/**
 * Front page. Every string below is the shipped default; an editor with ACF
 * overrides any of it from the page screen without touching the theme.
 */

get_header();

// Headline numbers repeat the benchmark section's values on purpose: one
// page, one measurement, one set of figures. Change them together.
$defaults = array(
	'stats'          => array(
		array( 'value' => '3.2', 'unit' => 'ms', 'label' => 'to serve a static file' ),
		array( 'value' => '18.8', 'unit' => 's', 'label' => 'to create a serving site' ),
		array( 'value' => '60', 'unit' => '', 'label' => 'tools in the agent API' ),
		array( 'value' => '0', 'unit' => '', 'label' => 'containers required' ),
	),
	'statement_one'       => 'NO DOCKER.',
	'statement_one_copy'  => 'Native processes on real files. Container tools pay the macOS filesystem tax on every request; agent-local lists your sites in 28 milliseconds and serves PHP in 4.6. After a reboot, everything returns on its own.',
	'statement_two'       => 'BUILT FOR AGENTS',
	'statement_two_copy'  => 'Most local tools assume a person is clicking. agent-local exposes the same engine to you and to Claude, Codex, or anything that speaks MCP. Create, import, snapshot, share, read the mail a form just sent. Failed calls name the fix, and nothing prompts for a password.',
	'features'       => array(
		array( 'title' => 'Captured mail', 'body' => 'Every email a site sends lands in a local inbox: password resets, form notifications, receipts. Agents can read it and assert on it.' ),
		array( 'title' => 'Automatic snapshots', 'body' => 'The database is saved before every destructive operation. A snapshot is a plain .sql.gz, and the one taken before a delete survives it.' ),
		array( 'title' => 'Public share links', 'body' => 'One command opens a verified public URL through a Cloudflare tunnel. No account needed, and client sites keep their production images.' ),
		array( 'title' => 'Branch previews', 'body' => 'Any git branch on its own domain beside the running site. Same database, copy-on-write files, ready in about ten seconds.' ),
		array( 'title' => 'Per-site PHP versions', 'body' => 'Each site runs its own pool, 7.4 through 8.5. Switching installs or repairs the runtime it needs, including releases Homebrew has dropped.' ),
		array( 'title' => 'Trusted HTTPS', 'body' => 'Every domain gets a certificate issued and trusted in the keychain the moment it is created or renamed. Sites and previews answer on 443 with no warning page.' ),
		array( 'title' => 'Streaming imports', 'body' => 'A 7 GB LocalWP or DDEV site imports in place with no copy step — DDEV projects move out of Docker entirely. Every stored domain is rewritten, serialized data included.' ),
		array( 'title' => 'Production media fallback', 'body' => 'Missing uploads redirect to your production origin, honouring the .htaccess rule already in the repo.' ),
		array( 'title' => 'WP_DEBUG without the ritual', 'body' => 'One flag turns debugging on with the log routed to a file and display kept off. Reproduce, then read the log. An agent does the same in two calls.' ),
		array( 'title' => 'A doctor that fixes', 'body' => 'Every health check reports the exact command that repairs it, and doctor --fix applies them all.' ),
	),
	'compare_rows'   => array(
		array( 'label' => 'Runs on', 'agentlocal' => 'native processes', 'localwp' => 'Electron + services', 'mamp' => 'bundled Apache/MySQL', 'ddev' => 'Docker containers' ),
		array( 'label' => 'Prerequisites', 'agentlocal' => 'none, installs what it needs', 'localwp' => 'app download', 'mamp' => 'app download', 'ddev' => 'Docker Desktop / Colima' ),
		array( 'label' => 'Agent control', 'agentlocal' => '60 MCP tools + HTTP API', 'localwp' => 'none', 'mamp' => 'none', 'ddev' => 'CLI with JSON output' ),
		array( 'label' => 'New WordPress site', 'agentlocal' => '19 seconds', 'localwp' => 'about a minute', 'mamp' => 'manual setup', 'ddev' => 'fast after first pull' ),
		array( 'label' => 'Memory for one site', 'agentlocal' => '52 MB', 'localwp' => '601 MB', 'mamp' => 'not measured', 'ddev' => '2.5 GB' ),
		array( 'label' => 'Trusted HTTPS', 'agentlocal' => 'automatic, every domain', 'localwp' => 'a Trust button per site', 'mamp' => 'by hand', 'ddev' => 'mkcert, once' ),
		array( 'label' => 'Branch previews', 'agentlocal' => 'own URLs, shared database', 'localwp' => 'none', 'mamp' => 'none', 'ddev' => 'second project by hand' ),
		array( 'label' => 'Outgoing mail capture', 'agentlocal' => 'yes, agents can read it', 'localwp' => 'yes', 'mamp' => 'no', 'ddev' => 'Mailpit' ),
		array( 'label' => 'Database snapshots', 'agentlocal' => 'automatic', 'localwp' => 'no', 'mamp' => 'no', 'ddev' => 'manual' ),
		array( 'label' => 'Public share links', 'agentlocal' => 'verified, no account', 'localwp' => 'account required', 'mamp' => 'no', 'ddev' => 'ngrok / cloudflared' ),
		array( 'label' => 'Coexists on port 80', 'agentlocal' => 'by design', 'localwp' => 'conflicts', 'mamp' => 'conflicts', 'ddev' => 'conflicts' ),
		array( 'label' => 'Beyond WordPress', 'agentlocal' => 'WordPress only, on purpose', 'localwp' => 'WordPress only', 'mamp' => 'any LAMP app', 'ddev' => 'Drupal, Laravel, TYPO3' ),
	),
	'install_steps'  => array(
		array( 'label' => 'install', 'command' => 'brew install jefrontv/tap/agent-local' ),
		array( 'label' => 'create', 'command' => 'agent-local create mysite' ),
		array( 'label' => 'agents', 'command' => 'agent-local connect' ),
	),
	'install_alt'    => 'curl -fsSL https://raw.githubusercontent.com/jefrontv/agent-local/main/install.sh | bash',
);
?>

<main id="main">

<!-- hero -->
<section class="hero">
	<div class="hero__field" aria-hidden="true"><pre class="wave-field"></pre></div>
	<h1 class="sr-only">agent-local: local WordPress for humans and agents</h1>
	<p class="hero__mark" aria-hidden="true">AGENT-<br class="mark__br">LOCAL</p>
</section>

<!-- stats -->
<section class="stats" id="speed">
	<p class="label"><span class="lamp" aria-hidden="true"></span>Measured, not estimated</p>
	<div class="stats__row">
		<?php foreach ( al_rows( 'stats', $defaults['stats'] ) as $s ) : ?>
		<div class="stat">
			<span class="stat__value" data-count="<?php echo esc_attr( $s['value'] ); ?>"><?php echo esc_html( $s['value'] ); ?></span><span class="stat__unit"><?php echo esc_html( $s['unit'] ?? '' ); ?></span>
			<span class="stat__label"><?php echo esc_html( $s['label'] ); ?></span>
		</div>
		<?php endforeach; ?>
	</div>
</section>

<!-- measured benchmarks -->
<?php
// Measured on this machine: Apple M3, macOS, September 2026. Same HTTP
// client, interleaved samples; medians of 30 for latency, 3 for lifecycle.
// An empty value renders as "not possible" rather than a bar.
$benchmark_defaults = array(
	array(
		'label' => 'Create a WordPress site', 'unit' => 's',
		'al' => '18.8', 'al_note' => 'median of three runs',
		'lw' => '', 'lw_note' => 'no CLI, by hand in the app',
		'dd' => '87.6', 'dd_note' => 'warm images; 215.7 s on first run',
	),
	array(
		'label' => 'Start a stopped site', 'unit' => 's',
		'al' => '0.4', 'al_note' => '',
		'lw' => '3.8', 'lw_note' => '',
		'dd' => '10.3', 'dd_note' => '',
	),
	array(
		'label' => 'List your sites from a script', 'unit' => 's',
		'al' => '0.03', 'al_note' => '',
		'lw' => '', 'lw_note' => 'no CLI',
		'dd' => '0.32', 'dd_note' => '',
	),
	array(
		'label' => 'Serve a static asset', 'unit' => 'ms',
		'al' => '3.2', 'al_note' => '',
		'lw' => '12.7', 'lw_note' => 'measured on a real client site',
		'dd' => '4.9', 'dd_note' => '',
	),
	array(
		'label' => 'Memory to serve one site', 'unit' => 'MB',
		'al' => '52', 'al_note' => 'the whole 34-site rack is 142 MB',
		'lw' => '601', 'lw_note' => 'including the 493 MB app window',
		'dd' => '2488', 'dd_note' => 'Docker VM, freshly booted',
	),
);
$bench_rows = al_rows( 'benchmarks', $benchmark_defaults );
?>
<section class="bench" id="benchmarks">
	<p class="label"><span class="lamp" aria-hidden="true"></span>Same machine, same client, same day</p>
	<h2 class="bench__heading">The numbers, side by side.</h2>
	<div class="bench__list">
		<?php foreach ( $bench_rows as $b ) :
			$vals = array_filter( array( (float) $b['al'], (float) $b['lw'], (float) $b['dd'] ) );
			$max  = $vals ? max( $vals ) : 1;
			if ( count( $vals ) < 2 ) {
				// A bar with nothing to compare against must not fill the
				// track and read as the slow one.
				$max *= 5;
			}
			$bars = array(
				array( 'who' => 'agent-local', 'value' => $b['al'], 'note' => $b['al_note'], 'us' => true ),
				array( 'who' => 'LocalWP', 'value' => $b['lw'], 'note' => $b['lw_note'], 'us' => false ),
				array( 'who' => 'DDEV', 'value' => $b['dd'], 'note' => $b['dd_note'], 'us' => false ),
			);
			// The ratio is against the nearest competitor that has a number:
			// the most conservative claim the measurements support.
			$ratio = '';
			$us    = (float) $b['al'];
			$rival = null;
			foreach ( array_slice( $bars, 1 ) as $bar ) {
				if ( '' === trim( (string) $bar['value'] ) ) {
					continue;
				}
				if ( null === $rival || (float) $bar['value'] < (float) $rival['value'] ) {
					$rival = $bar;
				}
			}
			if ( $rival && $us > 0 && (float) $rival['value'] > $us ) {
				$x     = (float) $rival['value'] / $us;
				$x     = $x >= 10 ? (string) round( $x ) : number_format( $x, 1 );
				$ratio = $x . '× ' . ( 'MB' === $b['unit'] ? 'less than' : 'faster than' ) . ' ' . $rival['who'];
			}
		?>
		<div class="bench__metric">
			<h3 class="bench__label"><?php echo esc_html( $b['label'] ); ?><span class="bench__lower">lower is better</span></h3>
			<?php foreach ( $bars as $bar ) : ?>
				<?php if ( '' === trim( (string) $bar['value'] ) ) : ?>
				<div class="bench__row bench__row--na">
					<span class="bench__who"><?php echo esc_html( $bar['who'] ); ?></span>
					<span class="bench__na"><?php echo esc_html( $bar['note'] ?: 'not possible' ); ?></span>
				</div>
				<?php else :
					$w = max( 1.2, round( (float) $bar['value'] / $max * 100, 1 ) ); ?>
				<div class="bench__row<?php echo $bar['us'] ? ' bench__row--us' : ''; ?>">
					<span class="bench__who"><?php echo esc_html( $bar['who'] ); ?></span>
					<span class="bench__track"><i class="bench__fill" style="--w:<?php echo esc_attr( $w ); ?>%"></i></span>
					<span class="bench__val"><?php echo esc_html( $bar['value'] . ' ' . $b['unit'] );
						if ( $bar['note'] ) : ?> <em><?php echo esc_html( $bar['note'] ); ?></em><?php endif;
						if ( $bar['us'] && $ratio ) : ?> <span class="bench__x"><?php echo esc_html( $ratio ); ?></span><?php endif; ?></span>
				</div>
				<?php endif; ?>
			<?php endforeach; ?>
		</div>
		<?php endforeach; ?>
	</div>
	<p class="bench__method">Apple M3, macOS, September 2026. One HTTP client for every provider, interleaved
		samples, medians of thirty for latency and three for lifecycle. DDEV 1.24 on Colima with 4 CPUs and
		6 GB; LocalWP 9. Memory is everything one site needs: daemon, database and PHP pool here;
		app window plus site services for LocalWP; the freshly booted VM for DDEV, not the 5 GB it
		peaks at while pulling images. Where a result was a tie we say so: fresh-install homepage
		time was 30 ms here, 29 ms on DDEV. Published as measured.</p>
</section>

<!-- statement one -->
<section class="statement">
	<h2 class="statement__display rv-scale"><?php echo esc_html( al_field( 'statement_one', $defaults['statement_one'] ) ); ?></h2>
	<p class="statement__copy"><?php echo esc_html( al_field( 'statement_one_copy', $defaults['statement_one_copy'] ) ); ?></p>
</section>

<!-- how it works: the anatomy, with a request walking through it -->
<section class="how" id="how">
	<p class="label"><span class="lamp" aria-hidden="true"></span>What is actually running</p>
	<h2 class="bench__heading">One engine. Three doors.</h2>
	<div class="how__map">
		<div class="how__node how__node--you">
			<span class="how__head">You, or an agent</span>
			<ul class="how__lines">
				<li><b>tui</b>the dashboard: sites, previews, runtimes, doctor</li>
				<li><b>cli</b>one command per action, scriptable</li>
				<li><b>mcp</b>60 tools over stdio, plus the HTTP API</li>
			</ul>
		</div>
		<div class="how__link how__link--in" aria-hidden="true"><span>http</span><i class="how__pkt"></i></div>
		<div class="how__node how__node--daemon">
			<span class="how__head">The daemon</span>
			<ul class="how__lines">
				<li><b>front</b>one listener on 80 and 443, routed by Host</li>
				<li><b>tls</b>a certificate per domain, trusted for you</li>
				<li><b>hosts</b>.test domains resolve with no port suffix</li>
				<li><b>db</b>one MariaDB, a schema and user per site</li>
				<li><b>mail</b>every outgoing message caught, nothing sent</li>
			</ul>
		</div>
		<div class="how__link how__link--out" aria-hidden="true"><span>fastcgi</span><i class="how__pkt"></i></div>
		<div class="how__node how__node--site">
			<span class="how__head">Each site</span>
			<ul class="how__lines">
				<li><b>fpm</b>its own PHP pool, 7.4 to 8.5, on a unix socket</li>
				<li><b>files</b>served where they already are, no copy</li>
				<li><b>@/</b>branch previews beside the checkout</li>
				<li><b>static</b>straight from disk, PHP streamed</li>
			</ul>
		</div>
	</div>
	<p class="how__copy">State is one JSON file, written under a lock and re-read when it changes, so the TUI, the CLI,
		the daemon and any number of agents act on the same truth at once. Processes carry pid files and are
		reaped on exit, so restarts never leave an orphan holding a socket.</p>
</section>

<!-- statement two: agents -->
<section class="statement" id="agents">
	<h2 class="statement__display rv-scale"><?php echo esc_html( al_field( 'statement_two', $defaults['statement_two'] ) ); ?></h2>
	<p class="statement__copy"><?php echo esc_html( al_field( 'statement_two_copy', $defaults['statement_two_copy'] ) ); ?></p>
	<div class="statement__term">
		<div class="term" aria-hidden="true">
			<div class="term__bar"><span class="term__lights"><i></i><i></i><i></i></span><span class="term__title">claude — ~/Sites/ferncreek</span></div>
			<div class="term__viewport"><pre id="term-screen">$ claude</pre></div>
			<div class="term__input"><span class="term__prompt">&gt;</span><span id="term-input"></span><span class="t-cursor"></span></div>
			<div class="term__status"><span>? for shortcuts</span><span>agent-local · 60 mcp tools</span></div>
		</div>
	</div>
	<p class="label label--center">create · import · snapshot · share · mail · previews</p>

	<?php
	// The harnesses that speak MCP. Icons are @lobehub/icons (MIT), inlined
	// so they take the page's palette; pi and Oh My Pi ship no public mark,
	// so they get type tiles in the same register.
	$harnesses = array(
		array( 'icon' => 'claude', 'name' => 'Claude Code' ),
		array( 'icon' => 'codex', 'name' => 'Codex' ),
		array( 'icon' => 'gemini', 'name' => 'Gemini CLI' ),
		array( 'icon' => 'antigravity', 'name' => 'Antigravity' ),
		array( 'glyph' => 'π', 'name' => 'pi' ),
		array( 'glyph' => 'omp', 'name' => 'Oh My Pi' ),
		array( 'icon' => 'zai', 'name' => 'Z.ai' ),
		array( 'icon' => 'qwen', 'name' => 'Qwen Code' ),
		array( 'icon' => 'deepseek', 'name' => 'DeepSeek' ),
		array( 'icon' => 'kimi', 'name' => 'Kimi CLI' ),
		array( 'icon' => 'copilot', 'name' => 'Copilot' ),
		array( 'icon' => 'cursor', 'name' => 'Cursor' ),
		array( 'icon' => 'windsurf', 'name' => 'Windsurf' ),
		array( 'icon' => 'grok', 'name' => 'Grok' ),
		array( 'icon' => 'mistral', 'name' => 'Mistral' ),
	);
	$harness_half = '';
	foreach ( $harnesses as $hx ) {
		$mark = isset( $hx['glyph'] )
			? '<span class="harness__glyph">' . esc_html( $hx['glyph'] ) . '</span>'
			: file_get_contents( get_template_directory() . '/assets/harness/' . $hx['icon'] . '.svg' );
		$harness_half .= '<li class="harness__item">' . $mark .
			'<span class="harness__name">' . esc_html( $hx['name'] ) . '</span></li>';
	}
	?>
	<div class="harness">
		<div class="harness__track">
			<ul class="harness__half" aria-label="Agent harnesses that speak MCP"><?php echo $harness_half; ?></ul>
			<ul class="harness__half" aria-hidden="true"><?php echo $harness_half; ?></ul>
		</div>
	</div>
</section>

<!-- features index -->
<section class="index" id="features">
	<p class="label"><span class="lamp" aria-hidden="true"></span>What ships in the binary</p>
	<div class="index__list">
		<?php foreach ( al_rows( 'features', $defaults['features'] ) as $i => $f ) : ?>
		<div class="index__row">
			<span class="index__no"><?php echo esc_html( str_pad( $i + 1, 2, '0', STR_PAD_LEFT ) ); ?></span>
			<h3 class="index__title"><?php echo esc_html( $f['title'] ); ?></h3>
			<p class="index__body"><?php echo esc_html( $f['body'] ); ?></p>
		</div>
		<?php endforeach; ?>
	</div>
</section>

<!-- comparison -->
<section class="compare" id="compare">
	<p class="label"><span class="lamp" aria-hidden="true"></span>Compared, honestly</p>
	<div class="compare__scroll">
		<table class="compare__table">
			<thead>
				<tr><th scope="col"><span class="sr-only">Capability</span></th><th scope="col">agent-local</th><th scope="col">LocalWP</th><th scope="col">MAMP</th><th scope="col">DDEV</th></tr>
			</thead>
			<tbody>
			<?php foreach ( al_rows( 'compare_rows', $defaults['compare_rows'] ) as $r ) : ?>
				<tr>
					<th scope="row"><?php echo esc_html( $r['label'] ); ?></th>
					<td class="compare__us"><?php echo esc_html( $r['agentlocal'] ); ?></td>
					<td><?php echo esc_html( $r['localwp'] ); ?></td>
					<td><?php echo esc_html( $r['mamp'] ); ?></td>
					<td><?php echo esc_html( $r['ddev'] ); ?></td>
				</tr>
			<?php endforeach; ?>
			</tbody>
		</table>
	</div>
</section>

<!-- install -->
<section class="install" id="install">
	<h2 class="statement__display statement__display--sm rv-scale">ONE BINARY.</h2>
	<div class="install__steps">
		<?php foreach ( al_rows( 'install_steps', $defaults['install_steps'] ) as $step ) : ?>
		<button class="install__step" type="button" data-copy="<?php echo esc_attr( $step['command'] ); ?>">
			<span class="install__k"><?php echo esc_html( $step['label'] ); ?></span>
			<code><?php echo esc_html( $step['command'] ); ?></code>
			<span class="install__copy" aria-hidden="true">copy</span>
		</button>
		<?php endforeach; ?>
	</div>
	<?php $alt = al_field( 'install_alt', $defaults['install_alt'] ); if ( $alt ) : ?>
	<button class="install__alt" type="button" data-copy="<?php echo esc_attr( $alt ); ?>">
		<span class="install__k">no homebrew</span>
		<code><?php echo str_replace( '/', '/<wbr>', esc_html( $alt ) ); ?></code>
	</button>
	<?php endif; ?>
	<p class="label label--center">macOS · Apple Silicon and Intel · MIT licensed</p>
	<span class="sr-only" aria-live="polite" id="copy-live"></span>
</section>

</main>

<?php get_footer(); ?>
