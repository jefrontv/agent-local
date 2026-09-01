<?php
/**
 * Fallback for anything that is not the front page: this theme is a
 * one-page site, so everything routes home.
 */
get_header();
?>
<main class="hero">
	<div class="hero__copy">
		<p>This page does not exist here. The whole site is the front page.</p>
		<p><a href="<?php echo esc_url( home_url( '/' ) ); ?>">← agent-local</a></p>
	</div>
</main>
<?php get_footer(); ?>
