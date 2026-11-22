"use strict";
const {app,BrowserWindow,Menu,MenuItem,ipcMain}=require("electron");
const fs=require("node:fs/promises");
const path=require("node:path");
const {pathToFileURL}=require("node:url");

const data=process.env.CAGE_LAN_CLIENT_DIR;
const select=process.env.CAGE_LAN_SELECTOR;
const selectorFile=path.join(process.resourcesPath,"app-dist","cage-selector.html");
const selectorURL=pathToFileURL(selectorFile).href;
const getRoutes=new Set(["info","servers"]);
const postRoutes=new Set(["scan","request","open","forget","local-history","delete-local-session"]);
const signal="cage-lan:show";
function owned(event){
    if(!event.senderFrame||event.senderFrame!==event.sender.mainFrame)return false;
    const u=new URL(event.senderFrame.url);
    return (u.protocol==="paseo:"&&u.hostname==="app"&&!u.port&&!u.username) || (!!select && u.href===selectorURL);
}
if(data){
    ipcMain.handle("cage-lan:call",async(event,route,args)=>{
        if(!owned(event)||!getRoutes.has(route)&&!postRoutes.has(route))throw Error("此操作需要从 Cage 工作台发起");
        const ctrl=JSON.parse(await fs.readFile(path.join(data,"control.json"),"utf8"));
        if(!/^127\.0\.0\.1:[0-9]{1,5}$/.test(ctrl.address))throw Error("本地连接服务地址无效");
        const origin="http://"+ctrl.address;
        const historyRoute=route==="local-history"||route==="delete-local-session";
        let body={id:typeof args?.id==="string"?args.id:""};
        if(historyRoute){
            if(select || !process.env.PASEO_HOME)throw Error("请从当前工作台的历史页面打开");
            const profile=path.dirname(path.resolve(process.env.PASEO_HOME));
            const id=path.basename(profile);
            const local=profile.toLowerCase()===path.dirname(path.resolve(data)).toLowerCase();
            if(!local && (!/^[0-9a-f]{64}$/.test(id) || profile.toLowerCase()!==path.join(path.resolve(data),"hosts",id).toLowerCase()))
                throw Error("工作台会话目录不属于当前配对");
            body={id:local?"local":id,serverId:args?.serverId,ids:args?.agentId?[args.agentId]:[]};
        }
        const result=await fetch(origin+"/api/"+route,{
            method:getRoutes.has(route)?"GET":"POST",
            headers:{"Authorization":"Bearer "+ctrl.token,"Origin":origin,"Content-Type":"application/json"},
            ...(postRoutes.has(route)?{body:JSON.stringify(body)}:{}),
            redirect:"error",signal:AbortSignal.timeout(historyRoute?65000:28000)
        });
        if(!result.ok){const v=await result.json().catch(()=>({}));throw Error(v.error||"局域网服务未就绪");}
        const v=await result.json();if(route==="info"){v.selector=!!select;v.auto=select==="auto";}
        if(route==="open"&&select)setTimeout(()=>app.quit(),800);
        return v;
    });
    // Surface the same server list inside a running workbench.
    app.on("browser-window-created",(_e,win)=>{
        win.webContents.on("did-finish-load",()=>{
            const menu=Menu.getApplicationMenu();
            if(!select && menu && !menu.getMenuItemById("cage-lan-menu")){
                menu.append(new MenuItem({id:"cage-lan-menu",label:"工作台",submenu:[{label:"局域网服务器…",accelerator:"CmdOrCtrl+Shift+L",click:()=>BrowserWindow.getFocusedWindow()?.webContents.send(signal)}]}));
                Menu.setApplicationMenu(menu);
            }
        });
    });
}
if(select&&data){
    app.setPath("userData",path.join(data,"Selector"));
    const single=app.requestSingleInstanceLock();
    if(!single)app.quit();
    else{
        let chooser;
        app.on("second-instance",()=>{if(chooser){chooser.restore();chooser.show();chooser.focus();chooser.webContents.send(signal);}});
        app.whenReady().then(async()=>{
            chooser=new BrowserWindow({title:"Cage 工作台 · 局域网服务器",width:850,height:570,minWidth:560,minHeight:400,backgroundColor:"#1d1e20",autoHideMenuBar:true,
                webPreferences:{preload:path.join(__dirname,"cage-lan-preload.js"),nodeIntegration:false,contextIsolation:true,sandbox:true}});
            chooser.webContents.setWindowOpenHandler(()=>({action:"deny"}));
            chooser.webContents.on("will-navigate",(e,url)=>{if(url!==selectorURL)e.preventDefault();});
            await chooser.loadFile(selectorFile);chooser.show();
        });
        app.on("window-all-closed",()=>app.quit());
    }
}else{require("./main.js");}
