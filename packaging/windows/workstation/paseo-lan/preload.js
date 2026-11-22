{
    const {contextBridge,ipcRenderer}=require("electron");
    contextBridge.exposeInMainWorld("cageWorkstations",{
        call:(name,body)=>ipcRenderer.invoke("cage-lan:call",name,body),
        onShow:listener=>{ipcRenderer.on("cage-lan:show",()=>listener());}
    });
}
