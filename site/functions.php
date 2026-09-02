<?php
/**
 * agent-local site theme.
 *
 * Content is ACF-editable but never ACF-dependent: every field read goes
 * through al_field()/al_rows(), which fall back to the defaults shipped in
 * front-page.php: the theme renders complete on a bare WordPress install.
 */

add_action( 'after_setup_theme', function () {
	add_theme_support( 'title-tag' );
} );

// The front page is the product, not the blog: its title says what the thing
// is regardless of what the WordPress install happens to be called.
add_filter( 'pre_get_document_title', function ( $title ) {
	if ( is_front_page() ) {
		return 'agent-local: local WordPress for humans and agents';
	}
	if ( get_query_var( 'al_docs' ) !== '' ) {
		$pages = require get_template_directory() . '/docs-content.php';
		$slug  = get_query_var( 'al_docs' );
		$name  = isset( $pages[ $slug ] ) ? $pages[ $slug ]['title'] : 'Docs';
		return $name . ' · agent-local docs';
	}
	return $title;
} );

add_action( 'wp_head', function () {
	if ( ! is_front_page() ) {
		return;
	}
	$ld = array(
		'@context'            => 'https://schema.org',
		'@type'               => 'SoftwareApplication',
		'name'                => 'agent-local',
		'operatingSystem'     => 'macOS',
		'applicationCategory' => 'DeveloperApplication',
		'description'         => 'One Go binary that creates, serves and manages WordPress sites on macOS. No Docker, no prerequisites, and a full agent API.',
		'url'                 => home_url( '/' ),
		'downloadUrl'         => 'https://github.com/jefrontv/agent-local/releases',
		'license'             => 'https://opensource.org/licenses/MIT',
		'offers'              => array( '@type' => 'Offer', 'price' => '0', 'priceCurrency' => 'USD' ),
	);
	echo '<script type="application/ld+json">' . wp_json_encode( $ld, JSON_UNESCAPED_SLASHES ) . '</script>' . "\n";
}, 5 );

// Docs: /docs/ and /docs/{slug}/ render the theme's own documentation —
// content ships in docs-content.php, so no pages in the database and no
// plugin. The query var carries the slug; "" is the index.
add_filter( 'query_vars', function ( $vars ) {
	$vars[] = 'al_docs';
	return $vars;
} );

add_action( 'init', function () {
	add_rewrite_rule( '^docs/?$', 'index.php?al_docs=index', 'top' );
	add_rewrite_rule( '^docs/([a-z0-9-]+)/?$', 'index.php?al_docs=$matches[1]', 'top' );
} );

add_filter( 'template_include', function ( $template ) {
	if ( get_query_var( 'al_docs' ) === '' ) {
		return $template;
	}
	$docs = get_template_directory() . '/docs.php';
	return file_exists( $docs ) ? $docs : $template;
} );

add_action( 'wp_enqueue_scripts', function () {
	$v = wp_get_theme()->get( 'Version' );
	wp_enqueue_style(
		'agent-local-fonts',
		'https://fonts.googleapis.com/css2?family=Archivo:wdth,wght@62..125,100..900&family=IBM+Plex+Mono:wght@400;500&display=swap',
		array(),
		null
	);
	wp_enqueue_style( 'agent-local', get_template_directory_uri() . '/dist/main.css', array(), $v );
	wp_enqueue_script( 'agent-local', get_template_directory_uri() . '/dist/main.js', array(), $v, array( 'strategy' => 'defer' ) );
} );

// Field groups live with the theme.
add_filter( 'acf/settings/save_json', fn() => get_template_directory() . '/acf-json' );
add_filter( 'acf/settings/load_json', function ( $paths ) {
	$paths[] = get_template_directory() . '/acf-json';
	return $paths;
} );

/**
 * A field value, or the shipped default when ACF is absent or the field is
 * empty. Editors override copy; the theme never renders a hole.
 */
function al_field( string $name, $default = '' ) {
	if ( function_exists( 'get_field' ) ) {
		$v = get_field( $name );
		if ( ! empty( $v ) ) {
			return $v;
		}
	}
	return $default;
}

/**
 * Repeater rows as plain arrays, or the shipped defaults. Repeaters need
 * ACF PRO; without it the defaults simply stand.
 */
function al_rows( string $name, array $defaults ): array {
	if ( function_exists( 'get_field' ) ) {
		$v = get_field( $name );
		if ( is_array( $v ) && $v ) {
			return $v;
		}
	}
	return $defaults;
}
