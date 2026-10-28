'use strict';
// Vecgrep Studio — preload. Exposes a minimal, safe API to the renderer.
const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('vecgrep', {
  binary: () => ipcRenderer.invoke('vg:binary'),
  projectGet: () => ipcRenderer.invoke('vg:project:get'),
  projectSet: (root) => ipcRenderer.invoke('vg:project:set', root),
  cli: (args, opts) => ipcRenderer.invoke('vg:cli', args, opts || {}),
  cliJson: (args, opts) => ipcRenderer.invoke('vg:cli:json', args, opts || {}),
  streamStart: (id, args, opts) => ipcRenderer.invoke('vg:stream:start', id, args, opts || {}),
  streamKill: (id) => ipcRenderer.invoke('vg:stream:kill', id),
  onStreamData: (fn) => {
    const h = (_e, payload) => fn(payload);
    ipcRenderer.on('vg:stream:data', h);
    return () => ipcRenderer.removeListener('vg:stream:data', h);
  },
  onStreamExit: (fn) => {
    const h = (_e, payload) => fn(payload);
    ipcRenderer.on('vg:stream:exit', h);
    return () => ipcRenderer.removeListener('vg:stream:exit', h);
  },
  mcpCall: (tool, args, opts) => ipcRenderer.invoke('vg:mcp:call', tool, args || {}, opts || {}),
  mcpTools: (opts) => ipcRenderer.invoke('vg:mcp:tools', opts || {}),
  mcpStatus: () => ipcRenderer.invoke('vg:mcp:status'),
  pickDirectory: () => ipcRenderer.invoke('vg:dialog:directory'),
  pickSave: (opts) => ipcRenderer.invoke('vg:dialog:save', opts || {}),
  fileWrite: (filePath, content) => ipcRenderer.invoke('vg:file:write', filePath, content),
  shellOpen: (target) => ipcRenderer.invoke('vg:shell:open', target),
  shellShow: (target) => ipcRenderer.invoke('vg:shell:show', target),
  fileRead: (filePath, startLine, endLine) => ipcRenderer.invoke('vg:file:read', filePath, startLine, endLine),
  fileExists: (filePath) => ipcRenderer.invoke('vg:file:exists', filePath),
});
