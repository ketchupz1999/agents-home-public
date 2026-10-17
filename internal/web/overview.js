(() => {
 'use strict';
 const root = document.querySelector('.overview');
 if (!root) return;
 const editor = document.getElementById('focus-editor');
 root.querySelectorAll('[data-open-focus]').forEach(link => link.addEventListener('click', () => {
  editor.open = true;
  editor.querySelector('select').focus({preventScroll:true});
 }));
 root.querySelectorAll('.focus-form').forEach(form => {
  form.addEventListener('submit', event => {
   const selected = [...form.querySelectorAll('[data-focus-slot]')].map(node => node.value).filter(Boolean);
   if (new Set(selected).size !== selected.length) {
    event.preventDefault();
    const error = form.querySelector('.focus-form-error');
    error.textContent = '两处请选择不同的事项。'; error.hidden = false;
   }
  });
 });
 const toggle = document.getElementById('overview-motion');
 if (!toggle) return;
 const preference = matchMedia('(prefers-reduced-motion: reduce)');
 let motion = !preference.matches, frameRequest = 0, last = 0;
 const actors = [];
 function draw(a) {
  const c=a.config, clip=c.animations[a.clip];
  const scale=Math.min(160/c.frameWidth,160/c.frameHeight), width=c.frameWidth*scale,height=c.frameHeight*scale;
  a.ctx.clearRect(0,0,160,160);a.ctx.imageSmoothingEnabled=!c.pixel;
  a.ctx.drawImage(a.image,a.frame*c.frameWidth,clip.row*c.frameHeight,c.frameWidth,c.frameHeight,(160-width)/2,160-height,width,height);
  a.canvas.dataset.frame=String(a.frame);a.canvas.dataset.action=a.clip;
 }
 function stop() {cancelAnimationFrame(frameRequest);frameRequest=0;last=0;}
 function start() {if (!frameRequest && motion && !document.hidden && actors.some(a=>a.ready && a.visible)) frameRequest=requestAnimationFrame(tick);}
 function tick(now) {
  frameRequest=0;
  if(!motion || document.hidden) {last=0;return;}
  const delta=last?Math.min(now-last,100):0;last=now;
  for(const a of actors) {
   if(!a.ready || !a.visible) continue;
   a.elapsed+=delta;
   const duration=a.clip==='idle' && a.frame===0?3300+a.offset:a.config.animations[a.clip].frameDurationsMs[a.frame];
   if(a.elapsed>=duration) {
    a.elapsed-=duration;a.frame++;
    if(a.frame>=a.config.animations[a.clip].frames) {a.frame=0;a.clip='idle';}
    draw(a);
   }
  }
  start();
 }
 const observer='IntersectionObserver' in window?new IntersectionObserver(entries=>{
  for(const entry of entries) {const a=actors.find(a=>a.root===entry.target);if(a)a.visible=entry.isIntersecting;}
  start();
 }):null;
 root.querySelectorAll('.overview-actor[data-manifest]').forEach((node,index)=>{
  let config;try {config=JSON.parse(node.dataset.manifest);}catch{return;}
  const canvas=node.querySelector('canvas'),ctx=canvas.getContext('2d');if(!ctx)return;
  const a={root:node,config,canvas,ctx,image:new Image(),ready:false,visible:true,frame:0,clip:'idle',elapsed:0,offset:index*650};actors.push(a);
  a.image.onload=()=>{
   if(a.image.naturalWidth!==config.frameWidth*config.columns || a.image.naturalHeight!==config.frameHeight*config.rows)return;
   a.ready=true;draw(a);canvas.hidden=false;node.querySelector('.actor-fallback').hidden=true;start();
  };
  a.image.src=config.atlas;
  if(observer)observer.observe(node);
  const react=()=>{if(!motion || !a.ready)return;a.clip='waiting';a.frame=0;a.elapsed=0;draw(a);start();};
  node.parentElement.addEventListener('pointerenter',event=>{if(event.pointerType==='mouse')react();});
  node.parentElement.addEventListener('focus',react);
 });
 function setMotion(value) {
  motion=value;toggle.textContent=motion?'暂停动效':'开启动效';toggle.setAttribute('aria-pressed',String(motion));
  stop();if(!motion)for(const a of actors) {a.clip='idle';a.frame=0;a.elapsed=0;if(a.ready)draw(a);}
  start();
 }
 toggle.hidden=actors.length===0;
 toggle.addEventListener('click',()=>setMotion(!motion));
 preference.addEventListener('change',event=>setMotion(!event.matches));
 document.addEventListener('visibilitychange',()=>{stop();start();});
 window.addEventListener('pagehide',stop);window.addEventListener('pageshow',start);
 setMotion(motion);
})();
