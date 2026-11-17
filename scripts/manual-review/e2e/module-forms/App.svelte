<script>
import ModuleDialog from '$lib/components/server/ModuleDialog.svelte';
import ModuleTemplateCreateDialog from '$lib/components/server/ModuleTemplateCreateDialog.svelte';
import {create} from '@bufbuild/protobuf';
import {ServerSchema} from '$lib/proto/carbonpanel/v1/common_pb';
import {ModuleTemplateSchema,ModuleSchema,ModulePortSchema,ModuleEventHookSchema,ModuleEventAction} from '$lib/proto/carbonpanel/v1/module_pb';
import {TriggeredEventType} from '$lib/proto/carbonpanel/v1/event_pb';
import {tick} from 'svelte';
import * as Tooltip from '$lib/components/ui/tooltip';
const volumes='[{"source":"/data/fixture","target":"/mount","read_only":true,"create_dir":false}]';
const ports=[create(ModulePortSchema,{name:'port',containerPort:8123,hostPort:0,protocol:'tcp',proxyEnabled:true})];
const hooks=[create(ModuleEventHookSchema,{event:TriggeredEventType.SERVER_START,action:ModuleEventAction.START,delaySeconds:3,condition:'fixture'})];
const template=create(ModuleTemplateSchema,{id:'template',name:'Fixture Template',dockerImage:'fixture/image:latest',defaultMemory:512,defaultEnv:'{" A ":" value "}',defaultVolumes:volumes,ports,defaultUid:'1000',defaultGid:'1001',defaultHooks:hooks,metadata:{fixture:'value'}});
const instance=create(ModuleSchema,{id:'instance',serverId:'server',serverProxyHostname:'fixture.example',name:'Fixture Instance',templateName:'Fixture Template',memory:512,ports:ports.map(p=>({...p,hostPort:8123})),envOverrides:template.defaultEnv,volumeOverrides:volumes,eventHooks:hooks,metadata:template.metadata});
const server=create(ServerSchema,{id:'server',proxyHostname:'fixture.example'});
let templateOpen=$state(false);let moduleOpen=$state(false);let mode=$state('create');
globalThis.__moduleUi={template:()=>{templateOpen=true},instance:(next)=>{mode=next;moduleOpen=true},flush:async()=>{await tick();await tick()}};
</script>
<Tooltip.Provider>
<ModuleTemplateCreateDialog bind:open={templateOpen} mode="edit" {template} onSuccess={()=>{}} />
<ModuleDialog bind:open={moduleOpen} {mode} {server} module={instance} templates={[template]} onSuccess={()=>{}} />

</Tooltip.Provider>
