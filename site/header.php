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
	<a class="nav__cta" href="#install">Install <span aria-hidden="true">↗</span></a>
</nav>
