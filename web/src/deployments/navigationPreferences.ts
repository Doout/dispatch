import { useEffect, useState } from "react";
import { DeploymentFilters } from "../routes";
export type NavigationPreferences={pins:string[];recent:string[];filters:{name:string;filters:DeploymentFilters}[]};
const empty=():NavigationPreferences=>({pins:[],recent:[],filters:[]});
const key=(identity:string)=>`dispatch.navigation.v1:${identity}`;
function read(identity:string):NavigationPreferences{
 try{const value=JSON.parse(localStorage.getItem(key(identity))||"null") as NavigationPreferences|null;
 return {pins:Array.isArray(value?.pins)?value.pins.filter(v=>typeof v==="string").slice(0,50):[],recent:Array.isArray(value?.recent)?value.recent.filter(v=>typeof v==="string").slice(0,8):[],filters:Array.isArray(value?.filters)?value.filters.filter(v=>typeof v.name==="string"&&v.filters&&typeof v.filters==="object").slice(0,20):[]};}catch{return empty();}
}
export function useNavigationPreferences(identity:string){
 const [preferences,setPreferences]=useState(()=>read(identity));
 useEffect(()=>{const refresh=()=>setPreferences(read(identity));refresh();window.addEventListener("dispatch-navigation-preferences",refresh);window.addEventListener("storage",refresh);return()=>{window.removeEventListener("dispatch-navigation-preferences",refresh);window.removeEventListener("storage",refresh);};},[identity]);
 function update(change:(current:NavigationPreferences)=>NavigationPreferences){const next=change(read(identity));try{localStorage.setItem(key(identity),JSON.stringify(next));}catch{/* Navigation continues when browser storage is unavailable. */}setPreferences(next);window.dispatchEvent(new Event("dispatch-navigation-preferences"));}
 return {preferences,update,togglePin:(id:string)=>update(p=>({...p,pins:p.pins.includes(id)?p.pins.filter(v=>v!==id):[...p.pins,id].slice(-50)}))};
}
