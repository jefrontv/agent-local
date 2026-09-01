<!doctype html>
<html <?php language_attributes(); ?>>
<head>
<meta charset="<?php bloginfo( 'charset' ); ?>">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="theme-color" content="#0e0e0c">
<meta name="description" content="One Go binary that creates, serves and manages WordPress sites on macOS. No Docker, no prerequisites, and a full agent API.">
<meta property="og:type" content="website">
<meta property="og:url" content="<?php echo esc_url( home_url( '/' ) ); ?>">
<meta property="og:title" content="agent-local: local WordPress for humans and agents">
<meta property="og:description" content="One Go binary for macOS. No Docker, no prerequisites, a site serving in under twenty seconds, and 59 MCP tools for the agents working beside you.">
<meta property="og:image" content="<?php echo esc_url( get_template_directory_uri() . '/assets/og-image.png' ); ?>">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta name="twitter:card" content="summary_large_image">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="icon" href="<?php echo esc_url( get_template_directory_uri() . '/assets/favicon.svg' ); ?>" type="image/svg+xml">
<link rel="icon" href="<?php echo esc_url( get_template_directory_uri() . '/assets/favicon-32.png' ); ?>" sizes="32x32" type="image/png">
<link rel="apple-touch-icon" href="<?php echo esc_url( get_template_directory_uri() . '/assets/apple-touch-icon.png' ); ?>">
<?php wp_head(); ?>
</head>
<body <?php body_class(); ?>>
<?php wp_body_open(); ?>

<a class="skip" href="#main">Skip to content</a>
<nav class="nav" aria-label="Page">
	<a class="nav__mark" href="<?php echo esc_url( home_url( '/' ) ); ?>">AGENT-LOCAL</a>
	<div class="nav__links">
		<a href="#speed">Speed</a>
		<a href="#benchmarks">Benchmarks</a>
		<a href="#how">How</a>
		<a href="#agents">Agents</a>
		<a href="#features">Features</a>
		<a href="#compare">Compare</a>
		<a class="nav__ext" href="https://github.com/jefrontv/agent-local">GitHub<span aria-hidden="true"> ↗</span></a>
	</div>
	<a class="nav__cta" href="#install">Install <span aria-hidden="true"><svg viewBox="0 0 14 14" width="11" height="11" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M3 11 11 3M4.5 3H11v6.5"/></svg></span></a>
</nav>
