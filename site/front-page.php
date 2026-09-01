<?php
/**
 * Front page. Every string below is the shipped default; an editor with ACF
 * overrides any of it from the page screen without touching the theme.
 */

get_header();

$defaults = array(
	'hero_intro'     => "Local WordPress that works the way you do.\n\nOne Go binary for macOS. No Docker, no prerequisites, a new site serving in fifteen seconds.",
	'hero_note'      => 'Built for developers, and for the agents working beside them.',
	'stats'          => array(
		array( 'value' => '2.9', 'unit' => 'ms', 'label' => 'to serve a static file' ),
		array( 'value' => '15', 'unit' => 's', 'label' => 'to create a serving site' ),
		array( 'value' => '59', 'unit' => '', 'label' => 'tools in the agent API' ),
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
		array( 'title' => 'Streaming imports', 'body' => 'A 7 GB LocalWP site imports in place with no copy step. Every stored domain is rewritten, serialized data included.' ),
		array( 'title' => 'Production media fallback', 'body' => 'Missing uploads redirect to your production origin, honouring the .htaccess rule already in the repo.' ),
		array( 'title' => 'A doctor that fixes', 'body' => 'Every health check reports the exact command that repairs it, and doctor --fix applies them all.' ),
	),
	'compare_rows'   => array(
		array( 'label' => 'Runs on', 'agentlocal' => 'native processes', 'localwp' => 'Electron + services', 'mamp' => 'bundled Apache/MySQL', 'ddev' => 'Docker containers' ),
		array( 'label' => 'Prerequisites', 'agentlocal' => 'none, installs what it needs', 'localwp' => 'app download', 'mamp' => 'app download', 'ddev' => 'Docker Desktop / Colima' ),
		array( 'label' => 'Agent control', 'agentlocal' => '59 MCP tools + HTTP API', 'localwp' => 'none', 'mamp' => 'none', 'ddev' => 'CLI with JSON output' ),
		array( 'label' => 'New WordPress site', 'agentlocal' => '15 seconds', 'localwp' => 'about a minute', 'mamp' => 'manual setup', 'ddev' => 'fast after first pull' ),
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
		array( 'label' => 'agents', 'command' => 'agent-local mcp --config' ),
	),
);
?>

<main>

<!-- hero -->
<section class="hero">
	<div class="hero__field" aria-hidden="true"><pre id="wave-field"></pre></div>
	<div class="hero__copy">
		<?php echo wp_kses_post( wpautop( al_field( 'hero_intro', $defaults['hero_intro'] ) ) ); ?>
		<p class="hero__note"><?php echo esc_html( al_field( 'hero_note', $defaults['hero_note'] ) ); ?></p>
	</div>
	<p class="hero__mark" aria-hidden="true">AGENT-LOCAL</p>
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
						if ( $bar['note'] ) : ?> <em><?php echo esc_html( $bar['note'] ); ?></em><?php endif; ?></span>
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

<!-- statement two: agents -->
<section class="statement" id="agents">
	<h2 class="statement__display rv-scale"><?php echo esc_html( al_field( 'statement_two', $defaults['statement_two'] ) ); ?></h2>
	<p class="statement__copy"><?php echo esc_html( al_field( 'statement_two_copy', $defaults['statement_two_copy'] ) ); ?></p>
	<div class="statement__term">
		<div class="term" aria-hidden="true">
			<pre id="term-screen">$ agent-local create demo
  ● Site ready: https://demo.test   15s</pre>
		</div>
	</div>
	<p class="label label--center">create · import · snapshot · share · mail · previews</p>
</section>

<!-- features index -->
<section class="index" id="features">
	<p class="label"><span class="lamp" aria-hidden="true"></span>What one command does</p>
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
				<tr><th></th><th>agent-local</th><th>LocalWP</th><th>MAMP</th><th>DDEV</th></tr>
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
	<p class="compare__note">If your week is Drupal and Laravel, DDEV is excellent. If your week is WordPress on a Mac, this table is your Tuesday.</p>
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
	<p class="label label--center">macOS · Apple Silicon and Intel · free and open source</p>
</section>

</main>

<?php get_footer(); ?>
