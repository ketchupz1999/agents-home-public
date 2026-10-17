(() => {
 'use strict';
 const page=document.querySelector('.team-page');if(!page)return;
 function mapLayout(map){
  const nodes=[...map.querySelectorAll('.team-node')],width=map.clientWidth;
  if(!width||!nodes.length)return;
  map.classList.add('is-mapped');
  const data=map.dataset.layout==='data',padding=width<500?13:30;
  const cols=width>720&&nodes.length>4?3:2;
  if(data){
   const nodeWidth=Math.min(185,Math.max(122,width*.26)),source=map.querySelector('.team-source');
   let top=24;
   nodes.forEach(node=>{node.style.width=nodeWidth+'px';node.style.left=padding+'px';node.style.top=top+'px';top+=node.offsetHeight+27;});
   const height=Math.max(310,top);map.style.height=height+'px';
   source.style.width=Math.min(174,Math.max(110,width*.25))+'px';source.style.right=padding+'px';source.style.top=(height-source.offsetHeight)/2+'px';
  }else{
   const nodeWidth=Math.min(172,(width-padding*2-(cols-1)*35)/cols);
   let top=30;
   for(let i=0;i<nodes.length;i+=cols){const row=nodes.slice(i,i+cols);let max=0;row.forEach((node,j)=>{node.style.width=nodeWidth+'px';node.style.left=(nodes.length===1?(width-nodeWidth)/2:padding+j*(width-padding*2-nodeWidth)/(cols-1))+'px';node.style.top=top+'px';max=Math.max(max,node.offsetHeight)});top+=max+88;}
   map.style.height=Math.max(290,top-58)+'px';
  }
  const base=map.getBoundingClientRect(),svg=map.querySelector('svg'),byID=new Map([...map.querySelectorAll('[data-node]')].map(n=>[n.dataset.node,n]));
  svg.setAttribute('viewBox',`0 0 ${base.width} ${base.height}`);
  svg.querySelectorAll('path').forEach(path=>{
   const from=byID.get(path.dataset.from),to=byID.get(path.dataset.to);if(!from||!to)return;
   const a=from.getBoundingClientRect(),b=to.getBoundingClientRect(),same=Math.abs(a.left-b.left)<4;
   let x1,y1,x2,y2,mx,my,d;
   y1=a.top+a.height/2-base.top;y2=b.top+b.height/2-base.top;
   if(same){x1=a.left-base.left;x2=b.left-base.left;mx=Math.max(4,x1-15);my=(y1+y2)/2;d=`M ${x1} ${y1} C ${mx} ${y1},${mx} ${y2},${x2} ${y2}`;}
   else{const forward=a.left<b.left;x1=(forward?a.right:a.left)-base.left;x2=(forward?b.left:b.right)-base.left;mx=(x1+x2)/2;my=(y1+y2)/2;d=`M ${x1} ${y1} C ${mx} ${y1},${mx} ${y2},${x2} ${y2}`;}
   path.setAttribute('d',d);
   const label=[...map.querySelectorAll('.team-edge-label')].find(n=>n.dataset.edge===path.dataset.edge);if(label){label.style.left=mx+'px';label.style.top=my+'px';}
  });
 }
 let queued=false;const maps=[...page.querySelectorAll('.team-map')];
 function schedule(){if(queued)return;queued=true;requestAnimationFrame(()=>{queued=false;maps.forEach(mapLayout)})}
 const layoutObserver=new ResizeObserver(schedule);maps.forEach(m=>layoutObserver.observe(m));schedule();
 const toggle=document.getElementById('team-motion');if(!toggle)return;
 const preference=matchMedia('(prefers-reduced-motion:reduce)'),actors=[],sheets=new Map();let motion=!preference.matches,request=0,last=0;
 function draw(a){const c=a.config,clip=c.animations[a.clip],scale=Math.min(160/c.frameWidth,160/c.frameHeight),w=c.frameWidth*scale,h=c.frameHeight*scale;a.ctx.clearRect(0,0,160,160);a.ctx.imageSmoothingEnabled=!c.pixel;a.ctx.drawImage(a.image,a.frame*c.frameWidth,clip.row*c.frameHeight,c.frameWidth,c.frameHeight,(160-w)/2,160-h,w,h);a.canvas.dataset.frame=String(a.frame);a.canvas.dataset.action=a.clip;}
 function stop(){cancelAnimationFrame(request);request=0;last=0;}
 function start(){if(!request&&motion&&!document.hidden&&actors.some(a=>a.ready&&a.visible))request=requestAnimationFrame(tick);}
 function tick(now){request=0;if(!motion||document.hidden){last=0;return}const delta=last?Math.min(now-last,100):0;last=now;for(const a of actors){if(!a.ready||!a.visible)continue;a.elapsed+=delta;const duration=a.clip==='idle'&&a.frame===0?3400+a.offset:a.config.animations[a.clip].frameDurationsMs[a.frame];if(a.elapsed>=duration){a.elapsed-=duration;a.frame++;if(a.frame>=a.config.animations[a.clip].frames){a.frame=0;a.clip='idle'}draw(a)}}start();}
 const visibility=new IntersectionObserver(entries=>{for(const e of entries){const a=actors.find(a=>a.root===e.target);if(a)a.visible=e.isIntersecting}start();});
 function sheet(config){if(!sheets.has(config.atlas))sheets.set(config.atlas,new Promise((resolve,reject)=>{const img=new Image();img.onload=()=>{if(img.naturalWidth!==config.columns*config.frameWidth||img.naturalHeight!==config.rows*config.frameHeight){reject(new Error('dimensions'));return}resolve(img)};img.onerror=()=>reject(new Error('image'));img.src=config.atlas}));return sheets.get(config.atlas);}
 page.querySelectorAll('.team-actor[data-manifest]').forEach((root,i)=>{let config;try{config=JSON.parse(root.dataset.manifest)}catch{return}const canvas=root.querySelector('canvas'),ctx=canvas&&canvas.getContext('2d');if(!ctx)return;const a={root,canvas,ctx,config,ready:false,visible:true,frame:0,clip:'idle',elapsed:0,offset:i*650};actors.push(a);visibility.observe(root);sheet(config).then(img=>{a.image=img;a.ready=true;draw(a);canvas.hidden=false;root.querySelector('.actor-fallback').hidden=true;start()}).catch(()=>{canvas.hidden=true;root.querySelector('.actor-fallback').hidden=false});const react=()=>{if(!motion||!a.ready)return;a.frame=0;a.clip='waiting';a.elapsed=0;draw(a);start()};root.parentElement.addEventListener('pointerenter',e=>{if(e.pointerType==='mouse')react()});root.parentElement.addEventListener('focus',react);});
 function setMotion(value){motion=value;stop();toggle.textContent=motion?'暂停动效':'开启动效';toggle.setAttribute('aria-pressed',String(motion));if(!motion)for(const a of actors){a.clip='idle';a.frame=0;a.elapsed=0;if(a.ready)draw(a)}start();}
 toggle.hidden=actors.length===0;toggle.addEventListener('click',()=>setMotion(!motion));preference.addEventListener('change',e=>setMotion(!e.matches));document.addEventListener('visibilitychange',()=>{stop();start()});window.addEventListener('pagehide',stop);window.addEventListener('pageshow',()=>{schedule();start()});setMotion(motion);
})();
