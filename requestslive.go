package main

import "strconv"

// The live half of the request page. Two behaviours, one script:
//
//   - a poll, once a second, for records newer than the newest row on
//     screen. It stops while the tab is hidden and while paused, and counts
//     what arrived so resuming can say how much.
//   - a detail panel per row, built from the record JSON already sitting in
//     the row, so opening one is instant and offline-safe.
//
// Rows are built in the browser with the same columns the server renders, so
// a live row and a reloaded row are indistinguishable.

// reqLiveScript returns the inline script for the request page. base is the
// browser-facing hub mount, so the poll asks the page it came from.
func reqLiveScript(base string) string {
	return `<script>(function(){
var BASE=` + strconv.Quote(base) + `,rows=document.getElementById("rows"),count=document.getElementById("count"),
pause=document.getElementById("pause"),none=document.getElementById("none"),
paused=sessionStorage.getItem("al-req-paused")==="1",missed=0,busy=false;

function esc(s){var d=document.createElement("div");d.textContent=s==null?"":String(s);return d.innerHTML}
function age(iso){var s=Math.max(0,(Date.now()-new Date(iso).getTime())/1000);
if(s<60)return Math.round(s)+"s ago";if(s<3600)return Math.round(s/60)+"m ago";
if(s<86400)return Math.round(s/3600)+"h ago";return Math.round(s/86400)+"d ago"}
function bytes(n){if(n>=1073741824)return (n/1073741824).toFixed(1)+"G";if(n>=1048576)return (n/1048576).toFixed(1)+"M";
if(n>=1024)return (n/1024).toFixed(1)+"K";return n+"B"}
function tag(s){return s>=500?"bad":s>=400?"warn":"mute"}

function row(r){
var tr=document.createElement("tr");tr.className="req";tr.dataset.id=r.id;
var errs=(r.php_errors||[]).map(function(l){return '<span class=phperr>'+esc(l)+'</span>'}).join("");
tr.innerHTML='<td class=age>'+esc(age(r.at))+'</td><td class=meth>'+esc(r.method)+'</td>'+
'<td class=path><button type=button class=open>'+esc(r.path)+'</button>'+errs+
'<script type="application/json" class=rec><\/script></td>'+
'<td class=lvl><span class="tag '+tag(r.status)+'">'+esc(r.status)+'</span></td>'+
'<td class=size>'+esc(Math.round(r.ms))+'ms</td><td class=size>'+esc(bytes(r.bytes))+'</td>'+
'<td class=src>'+esc(r.served)+'</td>';
tr.querySelector("script.rec").textContent=JSON.stringify(r);
return tr}

function newest(){var first=rows.querySelector("tr.req");return first?first.dataset.id:"0"}

function label(){
if(!pause)return;
pause.textContent=paused?(missed?"resume ("+missed+")":"resume"):"pause";
pause.className=paused?"on":""}

function drain(list){
if(!list.length)return;
if(none)none.remove();
list.slice().reverse().forEach(function(r){rows.insertBefore(row(r),rows.firstChild)});
var extra=rows.children.length-` + strconv.Itoa(hubReqLive) + `;
for(var i=0;i<extra;i++)rows.removeChild(rows.lastChild);
if(count)count.textContent=rows.children.length+" request"+(rows.children.length===1?"":"s")+" · newest first · live"}

// While paused the poll keeps asking, but nothing is prepended: the answer
// is only counted, so the button can say how many are waiting. The
// watermark does not move while paused, so each answer is the whole backlog
// rather than a delta to add up.
function poll(){
if(busy||document.hidden)return;
busy=true;
fetch(BASE+"/requests?format=json&after="+newest(),{headers:{"Accept":"application/json"}})
.then(function(r){return r.json()})
.then(function(d){var list=d.requests||[];
if(paused){missed=list.length;label()}else drain(list)})
.catch(function(){}).then(function(){busy=false})}

if(pause){
label();
pause.addEventListener("click",function(){
paused=!paused;sessionStorage.setItem("al-req-paused",paused?"1":"0");
if(!paused){missed=0;poll()}
label()})}

setInterval(poll,1000);
document.addEventListener("visibilitychange",function(){if(!document.hidden)poll()});

// One listener for every row, now and later.
rows.addEventListener("click",function(e){
var btn=e.target.closest("button.open");if(!btn)return;
var tr=btn.closest("tr.req"),next=tr.nextElementSibling;
if(next&&next.classList.contains("detail")){next.remove();tr.classList.remove("open");return}
var blob=tr.querySelector("script.rec");if(!blob)return;
var r;try{r=JSON.parse(blob.textContent)}catch(err){return}
var d=document.createElement("tr");d.className="detail";
d.innerHTML='<td></td><td colspan=6>'+detail(r)+'</td>';
tr.parentNode.insertBefore(d,tr.nextSibling);tr.classList.add("open")});

function pairs(list){
if(!list||!list.length)return '<p class=none>none recorded</p>';
return '<dl class=hdrs>'+list.map(function(p){
return '<dt>'+esc(p[0])+'</dt><dd>'+esc(p[1])+'</dd>'}).join("")+'</dl>'}

function detail(r){
var gen=[["url",(r.scheme||"https")+"://"+r.host+r.path],["method",r.method],["status",r.status],
["served by",r.served],["took",Math.round(r.ms)+"ms"],["sent",bytes(r.bytes)],
["protocol",r.proto||""],["client",r.remote_addr||""],["script",r.script||""]];
var out='<div class=panel>';
out+='<p class=phead>general</p>'+pairs(gen.filter(function(p){return p[1]!==""&&p[1]!=null}));
out+='<p class=phead>response headers</p>'+pairs(r.res_headers);
out+='<p class=phead>request headers</p>'+pairs(r.req_headers);
if(r.redacted&&r.redacted.length)
out+='<p class=none>withheld: '+esc(r.redacted.join(", "))+'</p>';
if(r.php_errors&&r.php_errors.length)
out+='<p class=phead>php errors</p><pre class=perr>'+r.php_errors.map(esc).join("\n")+'</pre>';
out+='</div>';
return out}
})()</script>`
}
