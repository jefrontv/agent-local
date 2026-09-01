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
