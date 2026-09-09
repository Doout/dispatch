import { expect, it } from "vitest";
import type { Overview } from "./api";
import { applyOverviewDelta } from "./overviewState";

it("applies field edits, insertions, removals, reorders, and escaped paths without changing the baseline", () => {
  const value = { projects: [{id:"a",state:"queued"},{id:"b",state:"ready"}], metadata:{"a/b~c":true,removed:1} };
  const state = {version:"v1",value:value as unknown as Overview};
  const next = applyOverviewDelta(state,{base:"v1",version:"v2",ops:[
    {op:"move",from:"/projects/1",path:"/projects/0"},
    {op:"replace",path:"/projects/1/state",value:"ready"},
    {op:"add",path:"/projects/1",value:{id:"c",state:"queued"}},
    {op:"remove",path:"/projects/2"},
    {op:"replace",path:"/metadata/a~1b~0c",value:false},
    {op:"remove",path:"/metadata/removed"},
    {op:"add",path:"/metadata/zero",value:0},
    {op:"add",path:"/metadata/nullable",value:null},
  ]});
  expect(next.value).toEqual({projects:[{id:"b",state:"ready"},{id:"c",state:"queued"}],metadata:{"a/b~c":false,zero:0,nullable:null}});
  expect(value.projects[0].state).toBe("queued");
  expect(() => applyOverviewDelta(next,{base:"v1",version:"v3",ops:[]})).toThrow("version mismatch");
});
it("rejects invalid paths without partially modifying the current state",()=>{
 const state={version:"v1",value:{projects:[]} as unknown as Overview};
 expect(()=>applyOverviewDelta(state,{base:"v1",version:"v2",ops:[{op:"add",path:"/projects/0",value:{}},{op:"add",path:"/__proto__/polluted",value:true}]})).toThrow();
 expect(state.value.projects).toEqual([]);
 expect(({} as any).polluted).toBeUndefined();
});
