(() => {
'use strict';
const demos = {
tree: '$ bp tree\n\nproject\n ├── research     idle\n ├── build        working\n │   └── tests    working\n └── review       idle\n\n$ bp peek build\nRunning the test suite…',
message: '$ bp msg build "Ready for review"\n\nAgent is working. Message queued.\n\n   research ─────→ queue ─────→ build\n                      ·\n               waiting for idle\n\nKeep working. The queue handles the handoff.',
connect: '$ bp con build\n\nAttach to the agent’s tmux session.\n\n  Your terminal → persistent session\n\nDetach when you need to.\nCome back to the same workspace.'
};
const terminal = document.querySelector('#terminal-content');
terminal.textContent = demos.tree;
document.querySelectorAll('[data-demo]').forEach(button => button.addEventListener('click', () => {
document.querySelectorAll('[data-demo]').forEach(b => { b.classList.toggle('active', b === button); b.setAttribute('aria-pressed', b === button ? 'true' : 'false'); });
terminal.textContent = demos[button.dataset.demo];
}));
document.querySelector('.copy').addEventListener('click', async event => {
const button = event.currentTarget;
try { await navigator.clipboard.writeText(document.querySelector('.install code').textContent); button.textContent = 'Copied ✓'; }
catch { document.querySelector('.install-note').textContent = 'Select the command above to copy it manually.'; }
});
const canvas = document.querySelector('canvas'), ctx = canvas.getContext('2d');
if (!ctx) return;
const mode = document.body.className, reduced = matchMedia('(prefers-reduced-motion: reduce)');
let paused = reduced.matches, visible = true, frame = 0, time = 0, last = 0, w = 0, h = 0, selected = -1;
const toggle = document.querySelector('.motion-toggle');
function syncToggle(){ toggle.textContent = paused ? 'Play motion ▷' : 'Pause motion Ⅱ'; toggle.setAttribute('aria-pressed',String(paused)); }
syncToggle();
const labels = ['coordinator','research','build','review','tests','design','docs'];
const details = ['coordinator / Keeps the project hierarchy in view.','research / Shares findings with the rest of the team.','build / Works in its own persistent terminal session.','review / Receives handoffs through the message queue.'];
const rgb = '43,92,217';
const positions = [];
function size(){ const box=canvas.getBoundingClientRect(); w=box.width; h=box.height; const dpr=Math.min(devicePixelRatio||1,2); canvas.width=w*dpr; canvas.height=h*dpr; ctx.setTransform(dpr,0,0,dpr,0,0); draw(); }
function line(a,b,alpha=.22){ctx.strokeStyle=`rgba(${rgb},${alpha})`;ctx.lineWidth=.7;ctx.beginPath();ctx.moveTo(a.x,a.y);if(mode==='blueprint'){ctx.lineTo((a.x+b.x)/2,a.y);ctx.lineTo((a.x+b.x)/2,b.y)}ctx.lineTo(b.x,b.y);ctx.stroke();}
function point(x,y,r,alpha=1){ctx.fillStyle=`rgba(${rgb},${alpha})`;ctx.beginPath();ctx.arc(x,y,r,0,Math.PI*2);ctx.fill();}
function draw(){
ctx.clearRect(0,0,w,h);positions.length=0;
const cx=w*.53,cy=h*.44,R=Math.min(w*.42,h*.36);
if(mode==='orbit'){
for(let k=0;k<3;k++){ctx.strokeStyle=`rgba(${rgb},${.12+k*.035})`;ctx.lineWidth=.7;ctx.beginPath();ctx.ellipse(cx,cy,R*(.65+k*.26),R*(.35+k*.16),-.5+k*.52,0,Math.PI*2);ctx.stroke()}
positions.push({x:cx,y:cy});
for(let i=1;i<7;i++){const a=i*Math.PI/3+time*.09;positions.push({x:cx+Math.cos(a)*R*(i%2?.97:.7),y:cy+Math.sin(a)*R*(i%2?.67:.95)})}
}else if(mode==='blueprint'){
const coords=w>700?[[.5,.22],[.12,.55],[.38,.55],[.65,.55],[.87,.8],[.12,.06],[.87,.06]]:[[.45,.4],[.13,.18],[.78,.31],[.77,.65],[.43,.76],[.12,.61],[.76,.06]];
coords.forEach(([x,y])=>positions.push({x:w*x,y:h*(.13+y*.78)}));
}else{
for(let i=0;i<20;i++){const a=i*2.39996+time*.012;const r=R*Math.sqrt((i+.8)/20);positions.push({x:w>700?w*.5+Math.cos(a)*w*.41:w*.48+Math.cos(a)*r,y:cy+Math.sin(a)*r+Math.cos(time*.22+i)*9})}
}
for(let i=1;i<positions.length;i++){
const a=positions[mode==='signal'?Math.max(0,i-3):0],b=positions[i];line(a,b,selected===i?.65:.21);
if(mode==='signal'&&i>2)line(positions[i-1],b,.12);
let t=(time*.12+i*.173)%1,x=a.x+(b.x-a.x)*t,y=a.y+(b.y-a.y)*t;
if(mode==='blueprint'){const mid=(a.x+b.x)/2;if(t<.25){x=a.x+(mid-a.x)*t*4;y=a.y}else if(t<.75){x=mid;y=a.y+(b.y-a.y)*(t-.25)*2}else{x=mid+(b.x-mid)*(t-.75)*4;y=b.y}}
point(x,y,2,.85);
}
positions.forEach((p,i)=>{
const active=selected===i;point(p.x,p.y,i===0?6:3.2,.9);
ctx.strokeStyle=`rgba(${rgb},${active?.8:.2})`;ctx.lineWidth=1;ctx.beginPath();ctx.arc(p.x,p.y,i===0?20:active?13:9,0,Math.PI*2);ctx.stroke();
if(i<7){ctx.font=`${i===0?'600 ':''}10px ui-monospace,monospace`;const label=labels[i];const width=ctx.measureText(label).width;ctx.fillStyle=getComputedStyle(document.body).getPropertyValue('--bg');ctx.fillRect(p.x+13,p.y-9,width+8,17);ctx.fillStyle=`rgba(${rgb},.9)`;ctx.fillText(label,p.x+17,p.y+3)}
});
if(mode==='orbit'){ctx.font='10px ui-monospace,monospace';ctx.fillStyle=`rgba(${rgb},.4)`;ctx.fillText('TMUX / PERSISTENT SESSIONS',cx-R*.7,cy+R+32)}
}
function tick(now){frame=0;if(paused||!visible||document.hidden)return;time+=last?Math.min((now-last)/1000,.05):0;last=now;draw();frame=requestAnimationFrame(tick)}
function run(){cancelAnimationFrame(frame);frame=0;last=0;draw();if(!paused&&visible&&!document.hidden)frame=requestAnimationFrame(tick)}
toggle.addEventListener('click',()=>{paused=!paused;syncToggle();run()});
reduced.addEventListener('change',()=>{paused=reduced.matches;syncToggle();run()});
function select(i){selected=i;document.querySelector('.node-detail').textContent=details[i]||`${labels[i]} / A connected agent in the example fleet.`;document.querySelectorAll('[data-agent]').forEach(b=>b.setAttribute('aria-pressed',String(Number(b.dataset.agent)===i)));draw()}
document.querySelectorAll('[data-agent]').forEach(b=>b.addEventListener('click',()=>select(Number(b.dataset.agent))));
canvas.addEventListener('click',event=>{const rect=canvas.getBoundingClientRect();let best=-1,dist=35;positions.slice(0,7).forEach((p,i)=>{const d=Math.hypot(event.clientX-rect.left-p.x,event.clientY-rect.top-p.y);if(d<dist){best=i;dist=d}});if(best>=0)select(best)});
canvas.addEventListener('pointermove',event=>{const rect=canvas.getBoundingClientRect();canvas.style.cursor=positions.slice(0,7).some(p=>Math.hypot(event.clientX-rect.left-p.x,event.clientY-rect.top-p.y)<25)?'pointer':'default'});
new ResizeObserver(size).observe(canvas);
new IntersectionObserver(entries=>{visible=entries[0].isIntersecting;run()}).observe(canvas);
document.addEventListener('visibilitychange',run);size();run();
})();
