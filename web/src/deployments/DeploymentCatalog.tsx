import { createContext, ReactNode, useContext, useEffect, useMemo, useState } from "react";
import { CheckCircle, Clock, Question, WarningCircle } from "@phosphor-icons/react";
import { Deployment, Overview, request } from "../api";
import { relative, short } from "../presentation";
import { AppRoute, routePath, shouldHandleNavigation } from "../routes";
import { catalogClient, CatalogItem, CatalogStatus } from "./catalogClient";

const CatalogContext = createContext<{items: CatalogItem[]; loading: boolean; error: string; onNavigate?: (route: AppRoute) => void}>({items: [], loading: false, error: ""});
export function DeploymentCatalogProvider({overview, children, onNavigate}: {overview: Overview | null; children: ReactNode; onNavigate?: (route: AppRoute) => void}) {
 const [items,setItems]=useState<CatalogItem[]>([]);const [loading,setLoading]=useState(true);const [error,setError]=useState("");
 const version=overview?.deployments.map(d=>`${d.id}:${d.state}`).join(",");
 useEffect(()=>{
  if(!overview){setItems([]);setLoading(false);return;}
  let alive=true;let busy=false;
  const update=async()=>{if(busy)return;busy=true;try{const data=await catalogClient.catalog();if(alive){setItems(data.items);setError("");}}catch(e){if(alive)setError(e instanceof Error?e.message:"Deployment status unavailable");}finally{busy=false;if(alive)setLoading(false);}};
  void update();const timer=window.setInterval(()=>void update(),30000);window.addEventListener("dispatch-observation-updated",update);
  return()=>{alive=false;window.clearInterval(timer);window.removeEventListener("dispatch-observation-updated",update);};
 },[version,overview?.identity?.id]);
 return <CatalogContext.Provider value={{items,loading,error,onNavigate}}>{children}</CatalogContext.Provider>;
}
export const useDeploymentCatalog=()=>useContext(CatalogContext);
export function catalogOverview(overview: Overview, items: CatalogItem[]): Overview {
 const merged=new Map(overview.deployments.map(d=>[d.id,d]));
 for(const item of items)for(const d of [item.current,item.latest])if(d&&!merged.has(d.id))merged.set(d.id,{...d,app:overview.apps.find(a=>a.id===d.appId),server:overview.servers.find(s=>s.id===item.targetId)});
 return {...overview,deployments:[...merged.values()].sort((a,b)=>b.createdAt.localeCompare(a.createdAt)||b.id.localeCompare(a.id))};
}
const label=(value:string)=>value.replaceAll("_"," ").replace(/^\w/,s=>s.toUpperCase());
export function DeploymentStatusPills({status}: {status?: CatalogStatus}) {
 if(!status)return <span className="catalog-status unavailable"><Question size={12}/>Status unavailable</span>;
 const stale=!!status.checkedAt&&Date.now()-Date.parse(status.checkedAt)>(status.staleAfterSeconds??900)*1000;
 const state=(kind:string,value:string,message?:string)=>{
  const good=["synced","healthy","in_sync","current"].includes(value);const bad=["drifted","out_of_sync","unhealthy","degraded","invalid","failed","error"].includes(value);
  const Icon=good?CheckCircle:bad?WarningCircle:Question;
  return <span className={`catalog-status ${good?"success":bad?"danger":"muted"}`} title={message||label(value)}><Icon size={12} weight={good||bad?"fill":"regular"}/>{kind}: {label(value)}</span>;
 };
 return <span className="catalog-status-group" aria-label="Observed application status">{state("Sync",status.configuration==="ready"?"synced":status.configuration)}{state("Drift",!status.supported?"not_supported":!status.checkedAt?"not_checked":status.drift,status.message)}{state("Health",!status.supported?"not_supported":!status.checkedAt?"not_checked":status.health,status.healthMessage||status.message)}<span className={`catalog-status ${stale?"stale":"muted"}`} title={status.checkedAt?new Date(status.checkedAt).toLocaleString():"No saved observation"}><Clock size={12}/>{status.checkedAt?`${stale?"Stale · ":""}${relative(status.checkedAt)}`:"Not checked"}</span></span>;
}
export function ApplicationDeploymentLink({appId,onNavigate,label="Open running release"}:{appId:string;onNavigate?:(route:AppRoute)=>void;label?:string}){
 const {items,onNavigate:defaultNavigate}=useDeploymentCatalog();const navigate=onNavigate??defaultNavigate;const item=items.find(i=>i.appId===appId);const d=item?.current??item?.latest;if(!d)return null;
 const route:AppRoute={view:"deployments",deploymentID:d.id};
 return <a className="deployment-open-link" href={routePath(route)} title={item?.current?"Last successful deployment; check health for its current runtime state":"Latest deployment attempt"} onClick={e=>{if(navigate&&shouldHandleNavigation(e)){e.preventDefault();navigate(route);}}}>{item?.current?label:"Open latest attempt"}</a>;
}
export function DeploymentIdentity({deployment,overview,onNavigate}:{deployment:Deployment;overview?:Overview;onNavigate?:(id:string)=>void}){
 const {items}=useDeploymentCatalog();const [detail,setDetail]=useState<CatalogItem>();const [unavailable,setUnavailable]=useState(false);
 useEffect(()=>{let alive=true;setDetail(undefined);setUnavailable(false);void catalogClient.identity(deployment.id).then(v=>{if(alive)setDetail(v);}).catch(()=>{if(alive)setUnavailable(true);});return()=>{alive=false;};},[deployment.id,deployment.state]);
 const item=detail??items.find(i=>i.appId===deployment.appId);const app=deployment.app??overview?.apps.find(a=>a.id===deployment.appId);
 const kind=item?.current?.id===deployment.id?"Running release":item?.latest?.id===deployment.id?"Latest attempt":item?.latest?"Historical deployment":"Deployment";
 const links=useMemo(()=>[item?.current&&item.current.id!==deployment.id?{d:item.current,label:"Running release"}:null,item?.latest&&item.latest.id!==deployment.id&&item.latest.id!==item.current?.id?{d:item.latest,label:"Latest attempt"}:null].filter(v=>v!==null),[item,deployment.id]);
 return <section className="deployment-identity" aria-label="Deployment identity"><div className="deployment-identity-main"><strong>{app?.name||item?.appName||deployment.appId}</strong>{item?.environment&&<span>{item.environment}</span>}<span title="Target recorded when this deployment was accepted">{detail?.targetName||detail?.targetId||(!detail?unavailable?"Target unavailable":"Loading target…":"Target not recorded")}</span><code title={deployment.commitSha}>{short(deployment.commitSha)||"No revision"}</code><ApplicationOwnerLabel appId={deployment.appId}/><span className={`deployment-identity-state ${deployment.state}`}>{deployment.state}</span></div><div className="deployment-identity-context"><span title={kind==="Running release"?"Last successful deployment. Runtime health is observed separately.":undefined}>{kind}</span><time dateTime={deployment.createdAt}>{relative(deployment.createdAt)}</time>{links.map(({d,label})=><a key={d.id} href={routePath({view:"deployments",deploymentID:d.id})} onClick={e=>{if(onNavigate&&shouldHandleNavigation(e)){e.preventDefault();onNavigate(d.id);}}}>{label} <code>{short(d.commitSha)}</code></a>)}</div></section>;
}

export function ApplicationOwnerLabel({appId}:{appId:string}) {
 const [name,setName]=useState("");
 useEffect(()=>{let alive=true;setName("");void request<{displayName?:string}>(`/api/v1/apps/${encodeURIComponent(appId)}/owner`).then(value=>{if(alive)setName(value.displayName??"");}).catch(()=>{});return()=>{alive=false;};},[appId]);
 return name?<span className="application-owner-label" title="Responsible person or team; ownership does not grant access">Owner: {name}</span>:null;
}
