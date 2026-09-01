<!doctype html>
<html <?php language_attributes(); ?>>
<head>
<meta charset="<?php bloginfo( 'charset' ); ?>">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="description" content="One Go binary that creates, serves and manages WordPress sites on macOS. No Docker, no prerequisites, and a full agent API.">
<?php wp_head(); ?>
</head>
<body <?php body_class(); ?>>
<?php wp_body_open(); ?>

<nav class="nav">
	<a class="nav__mark" href="<?php echo esc_url( home_url( '/' ) ); ?>">AGENT-LOCAL</a>
	<div class="nav__links">
		<a href="#speed">Speed</a>
		<a href="#benchmarks">Benchmarks</a>
		<a href="#agents">Agents</a>
		<a href="#features">Features</a>
		<a href="#compare">Compare</a>
	</div>
	<a class="nav__cta" href="#install">Install <span aria-hidden="true"><svg viewBox="0 0 14 14" width="11" height="11" fill="none" stroke="currentColor" stroke-width="1.6"><path d="M3 11 11 3M4.5 3H11v6.5"/></svg></span></a>
</nav>
