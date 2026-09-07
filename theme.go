package main

import "html"

// The tool pages — hub, inbox, database GUI — follow the OS colour scheme
// and can be pinned light or dark from their top bar. The palette itself is
// CSS: every token is light-dark(), and color-scheme on <html> is the whole
// switch. This script only decides which color-scheme applies: it reads the
// pin from localStorage before first paint (no flash), mounts the toggle,
// and keeps other open tabs in step through the storage event. Without JS
// the pages still follow the system.
//
// One key for all three pages: they share an origin, so a pin made in the
// inbox holds in Adminer.
const themeKey = "agent-local-theme"

// themeJS is the script body. It reads its mount — the selector of the bar
// element the toggle is appended to once the DOM exists — from the data-mount
// attribute of its own <script>; a page without that element gets the scheme
// but no button.
const themeJS = `(function(){
var K="` + themeKey + `",root=document.documentElement,mount=document.currentScript.dataset.mount,
next={auto:"light",light:"dark",dark:"auto"};
function mode(){var v=localStorage.getItem(K);return next[v]?v:"auto"}
function apply(){var m=mode();m==="auto"?root.removeAttribute("data-theme"):root.setAttribute("data-theme",m);
var b=document.getElementById("theme");if(b)b.textContent="theme: "+m}
apply();
document.addEventListener("DOMContentLoaded",function(){var at=mount&&document.querySelector(mount);
if(at&&!document.getElementById("theme"))at.insertAdjacentHTML("beforeend",'<button type=button id=theme title="colour scheme: follows the system, or pinned light or dark. Click to cycle."></button>');
apply()});
document.addEventListener("click",function(e){if(!e.target.closest("#theme"))return;var n=next[mode()];
n==="auto"?localStorage.removeItem(K):localStorage.setItem(K,n);apply()});
addEventListener("storage",function(e){if(e.key===K)apply()});
})()`

// themeScript returns the inline <script> for a tool page this binary
// renders. Adminer builds its own tag: its CSP wants a nonce on every script.
func themeScript(mount string) string {
	return `<script data-mount="` + html.EscapeString(mount) + `">` + themeJS + `</script>`
}
