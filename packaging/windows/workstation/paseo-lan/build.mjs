// Overlay the Cage workstation integration on the installed upstream Paseo 0.17.2 distribution.
import * as fs from "node:fs";
import path from "node:path";
import {fileURLToPath} from "node:url";
import {createHash} from "node:crypto";
import {createCageNavigation} from "./navigation.mjs";

const here=path.dirname(fileURLToPath(import.meta.url));
const macResources=process.argv[2]==="--macos-resources";
const workstation=path.resolve(process.argv[2]||path.join(here,"../../../../../Workstation"));
const source=macResources?path.resolve(process.argv[3]):path.join(workstation,"Paseo");
const target=macResources?source:path.resolve(process.argv[3]||path.join(workstation,"PaseoLAN"));
const resources=macResources?"":"resources";
const asar=path.join(resources,"app.asar");
const upstream=fs.readFileSync(path.join(source,asar));
const oldBase=8+upstream.readUInt32LE(4);
const header=JSON.parse(upstream.toString("utf8",16,16+upstream.readUInt32LE(12)));
function entry(name){return name.split("/").reduce((n,k)=>n.files[k],header);}
function read(name){const e=entry(name),offset=oldBase+Number(e.offset);return upstream.subarray(offset,offset+e.size);}
const meta=JSON.parse(read("package.json"));
if(meta.version!=="0.17.2"||meta.main!=="dist/main.js"||!entry("dist/preload.js").size)throw Error("Expected the original Paseo 0.17.2 distribution");
const sha=b=>createHash("sha256").update(b).digest("hex");
const extras=[];
let next=upstream.length-oldBase;
function put(name,data){
    const parts=name.split("/");const leaf=parts.pop();const dest=parts.reduce((n,k)=>n.files[k],header).files;
    const bytes=Buffer.from(data),blockSize=4*1024*1024,blocks=[];
    for(let i=0;i<bytes.length;i+=blockSize)blocks.push(sha(bytes.subarray(i,i+blockSize)));
    dest[leaf]={size:bytes.length,offset:String(next),integrity:{algorithm:"SHA256",hash:sha(bytes),blockSize,blocks}};
    next+=bytes.length;extras.push(bytes);
}
const pre=fs.readFileSync(path.join(here,"preload.js"));
put("dist/cage-lan-main.cjs",fs.readFileSync(path.join(here,"main.cjs")));
put("dist/cage-lan-preload.js",pre);
put("dist/preload.js",Buffer.concat([read("dist/preload.js"),Buffer.from("\n"),pre]));
meta.main="dist/cage-lan-main.cjs";
put("package.json",JSON.stringify(meta));
const json=Buffer.from(JSON.stringify(header)),padded=Math.ceil(json.length/4)*4;
const prefix=Buffer.alloc(16+padded);
prefix.writeUInt32LE(4,0);prefix.writeUInt32LE(8+padded,4);prefix.writeUInt32LE(4+padded,8);prefix.writeUInt32LE(json.length,12);json.copy(prefix,16);
// Bind the packaged v0.17.2 history screen's normal refetch callback. This keeps search, focus,
// unrelated tabs and drafts intact when the external deletion RPC removes a history entry.
const appDist=path.join(resources,"app-dist");
const jsDir=path.join(appDist,"_expo/static/js/web");
const screenHook="refreshAll:ee}=(0,T.useAgentHistory)(U),se=Q&&W.length>0;";
const screens=fs.readdirSync(path.join(source,jsDir))
    .filter(name=>/^index-.*\.js$/.test(name))
    .map(name=>path.join(jsDir,name))
    .map(file=>({file, text:fs.readFileSync(path.join(source,file),"utf8")}))
    .filter(({text})=>text.includes(screenHook));
if(screens.length!==1)throw Error("Expected one Paseo 0.17.2 workbench screen");
const [{file:screenFile,text:originalScreen}]=screens;
function replaceOnce(input, marker, replacement) {
    const position = input.indexOf(marker);
    if (position < 0 || input.indexOf(marker, position + marker.length) !== -1) {
        throw Error("Unsupported Paseo 0.17.2 workbench marker: " + marker);
    }
    return input.slice(0, position) + replacement + input.slice(position + marker.length);
}
let screen = replaceOnce(originalScreen, screenHook, screenHook+'(0,o.useEffect)(()=>{const refresh=()=>{void ee()};window.addEventListener("cage-history-changed",refresh);return()=>window.removeEventListener("cage-history-changed",refresh)},[ee]);');

// Register the Cage screen in the distributed Metro route table. Everything below is scoped to
// this exact release. The row and the page use Paseo's own React, router, theme and window chrome.
const routeId = 65000;
if (screen.includes("}," + routeId + ",[")) throw Error("Cage route module ID is already in use");
const routeModule = `__d(function(global, require, i, a, module, exports, deps) {
    const navigation = require(deps[1]);
    module.exports = (${createCageNavigation.toString()})({
      React: require(deps[0]), router: navigation.router, usePathname: navigation.usePathname,
      useIsFocused: require(deps[2]).useIsFocused, useUnistyles: require(deps[3]).useUnistyles,
      SidebarHeaderRow: require(deps[4]).SidebarHeaderRow, MenuHeader: require(deps[5]).MenuHeader,
      ServerIcon: require(deps[6]).Server
    });
},${routeId},[35,488,4675,160,4098,4096,1396]);\n`;
const manifestStart = '__d(function(g,r,i,a,m,e,d){const t=Object.defineProperties({},{"./_layout.tsx":';
screen = replaceOnce(screen, manifestStart, routeModule + manifestStart);
screen = replaceOnce(screen, '"./schedules.tsx":', `"./cage-servers.tsx":{enumerable:!0,get:()=>r(${routeId})},"./schedules.tsx":`);
screen = replaceOnce(screen, '"/schedules"===s||h', '"/schedules"===s||"/cage-servers"===s||h');
screen = replaceOnce(screen, 'children:[s,c,l,u,d,h,S]', 'children:[s,c,l,u,d,h,S,(0,Je.jsx)(j.Stack.Screen,{name:"cage-servers"})]');
screen = replaceOnce(screen, 'n=o.filter(x),t[0]=o', `n=r(${routeId}).withSidebarItem(o.filter(x)),t[0]=o`);
screen = replaceOnce(screen, 'const P={"new-workspace":(0,c.memo)', `const P={"cage-servers":r(${routeId}).SidebarRow,"new-workspace":(0,c.memo)`);
const htmlEntry=path.join(appDist,"index.html");
const changed=new Set([asar,htmlEntry,screenFile].map(file=>file.split(path.sep).join("/")));
function linkTree(rel){
    const base=path.join(source,rel),out=path.join(target,rel);fs.mkdirSync(out,{recursive:true});
    for(const item of fs.readdirSync(base,{withFileTypes:true})){
        const name=rel?rel+"/"+item.name:item.name;
        if(changed.has(name)||item.name==="Uninstall Paseo.exe")continue;
        if(item.isDirectory())linkTree(name);
        else if(item.isFile()){
            const dst=path.join(target,name);if(fs.existsSync(dst))continue;
            try{fs.linkSync(path.join(source,name),dst);}catch(e){if(e.code!=="EXDEV"&&e.code!=="EPERM")throw e;fs.copyFileSync(path.join(source,name),dst);}
        }
    }
}
if(!macResources)linkTree("");
function publish(dest,parts){
    const tmp=path.join(path.dirname(dest),"."+path.basename(dest)+"."+process.pid+".tmp");
    const fd=fs.openSync(tmp,"wx");
    try{for(const data of parts){let offset=0;while(offset<data.length)offset+=fs.writeSync(fd,data,offset,data.length-offset);}fs.fsyncSync(fd);}finally{fs.closeSync(fd);}
    try{fs.renameSync(tmp,dest);}finally{if(fs.existsSync(tmp))fs.unlinkSync(tmp);}
}
publish(path.join(target,asar),[prefix,upstream.subarray(oldBase),...extras]);
publish(path.join(target,screenFile),[Buffer.from(screen)]);
const dist=path.join(target,appDist);
for(const [src,dst] of [["stations.js","cage-stations.js"],["stations.css","cage-stations.css"],["history.js","cage-history.js"],["history.css","cage-history.css"]])publish(path.join(dist,dst),[fs.readFileSync(path.join(here,src))]);
const selector=fs.readFileSync(path.join(here,"selector.html"),"utf8");
publish(path.join(dist,"cage-selector.html"),[Buffer.from(macResources?replaceOnce(selector,'class="cage-selector"','class="cage-selector cage-selector-macos"'):selector)]);
const html=fs.readFileSync(path.join(source,htmlEntry),"utf8")
    .replace("</head>",'<link rel="stylesheet" href="/cage-stations.css"><link rel="stylesheet" href="/cage-history.css"></head>')
    .replace("</body>",'<script defer src="/cage-stations.js"></script><script defer src="/cage-history.js"></script></body>');
publish(path.join(dist,"index.html"),[Buffer.from(html)]);
console.log("Built Cage workstation with in-app LAN selection:",target);
