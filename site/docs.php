<?php
/**
 * Docs template. Content ships in docs-content.php; nothing here depends on
 * ACF or the database. /docs/ is the index, /docs/{slug}/ a page.
 */

$pages   = require get_template_directory() . '/docs-content.php';
$current = get_query_var( 'al_docs', 'index' );
if ( ! isset( $pages[ $current ] ) ) {
	$current = 'index';
}
$page  = $pages[ $current ];
$slugs = array_keys( $pages );
$pos   = (int) array_search( $current, $slugs, true );
$next  = array();
for ( $i = 1; $i <= 3; $i++ ) {
	$s = $slugs[ ( $pos + $i ) % count( $slugs ) ];
	$next[ $s ] = $pages[ $s ];
}

/**
 * Inline markup: `code` and **strong**, everything else escaped.
 */
function al_docs_inline( $text ) {
	$parts = preg_split( '/(`[^`]*`|\*\*[^*]+\*\*)/', $text, -1, PREG_SPLIT_DELIM_CAPTURE );
	$out   = array();
	foreach ( $parts as $p ) {
		if ( $p === '' ) {
			continue;
		}
		if ( strlen( $p ) > 2 && $p[0] === '`' && substr( $p, -1 ) === '`' ) {
			$out[] = '<code>' . esc_html( substr( $p, 1, -1 ) ) . '</code>';
		} elseif ( strlen( $p ) > 4 && substr( $p, 0, 2 ) === '**' && substr( $p, -2 ) === '**' ) {
			$out[] = '<strong>' . esc_html( substr( $p, 2, -2 ) ) . '</strong>';
		} else {
			$out[] = esc_html( $p );
		}
	}
	return implode( '', $out );
}

function al_docs_url( $slug ) {
	return home_url( '/docs/' . ( $slug === 'index' ? '' : $slug . '/' ) );
}

get_header();
?>
<main id="main" class="docs">
	<div class="docs__head">
		<p class="label"><span class="lamp" aria-hidden="true"></span>documentation</p>
		<h1 class="docs__title"><?php echo esc_html( $page['title'] ); ?></h1>
		<p class="docs__intro"><?php echo wp_kses_post( al_docs_inline( $page['intro'] ) ); ?></p>
	</div>

	<div class="docs__body">
		<nav class="docs__nav" aria-label="Documentation">
			<p class="label label--nav">Docs</p>
			<?php foreach ( $pages as $slug => $p ) : ?>
				<a href="<?php echo esc_url( al_docs_url( $slug ) ); ?>"<?php echo $slug === $current ? ' aria-current="page"' : ''; ?>>
					<?php echo esc_html( $p['title'] ); ?>
				</a>
			<?php endforeach; ?>
		</nav>

		<article class="docs__page">
			<?php
			foreach ( $page['sections'] as $s ) {
				switch ( $s[0] ) {
					case 'h':
						echo '<h2>' . esc_html( $s[1] ) . '</h2>';
						break;
					case 'p':
						echo '<p>' . wp_kses_post( al_docs_inline( $s[1] ) ) . '</p>';
						break;
					case 'pre':
						echo '<pre class="docs__pre"><code>' . esc_html( $s[1] ) . '</code></pre>';
						break;
					case 'ul':
						echo '<ul>';
						foreach ( $s[1] as $li ) {
							echo '<li>' . wp_kses_post( al_docs_inline( $li ) ) . '</li>';
						}
						echo '</ul>';
						break;
					case 'ol':
						echo '<ol>';
						foreach ( $s[1] as $li ) {
							echo '<li>' . wp_kses_post( al_docs_inline( $li ) ) . '</li>';
						}
						echo '</ol>';
						break;
					case 'table':
						echo '<div class="docs__tablewrap"><table>';
						echo '<thead><tr>';
						foreach ( $s[1] as $th ) {
							echo '<th>' . esc_html( $th ) . '</th>';
						}
						echo '</tr></thead><tbody>';
						foreach ( $s[2] as $row ) {
							echo '<tr>';
							foreach ( $row as $i => $td ) {
								echo ( 0 === $i ? '<td class="docs__k">' : '<td>' ) . wp_kses_post( al_docs_inline( $td ) ) . '</td>';
							}
							echo '</tr>';
						}
						echo '</tbody></table></div>';
						break;
					case 'note':
						echo '<p class="docs__note">' . wp_kses_post( al_docs_inline( $s[1] ) ) . '</p>';
						break;
				}
			}
			?>
			<nav class="docs__next" aria-label="More pages">
				<p class="label">Keep reading</p>
				<div class="docs__nextlinks">
					<?php foreach ( $next as $slug => $p ) : ?>
						<a href="<?php echo esc_url( al_docs_url( $slug ) ); ?>"><?php echo esc_html( $p['title'] ); ?><span aria-hidden="true"> →</span></a>
					<?php endforeach; ?>
				</div>
			</nav>
		</article>
	</div>
</main>
<?php get_footer();
