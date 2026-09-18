-- Durable, compact completion events. No logs, credentials or configuration snapshots.
CREATE TABLE analytics_outbox (
 event_id BIGSERIAL PRIMARY KEY, kind TEXT NOT NULL, entity_id TEXT NOT NULL,
 project_id TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL,
 started_at TEXT NOT NULL, finished_at TEXT NOT NULL, reused INTEGER NOT NULL
);
CREATE FUNCTION analytics_deployment_completion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IN ('succeeded','failed','cancelled') THEN
  IF TG_OP = 'INSERT' THEN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'deployment',NEW.id,a.project_id,a.name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),0 FROM apps a WHERE a.id=NEW.app_id;
  ELSIF (OLD.state<>NEW.state OR COALESCE(OLD.finished_at,'')<>COALESCE(NEW.finished_at,'')) THEN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'deployment',NEW.id,a.project_id,a.name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),0 FROM apps a WHERE a.id=NEW.app_id;
  END IF;
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER analytics_deployment_completion AFTER INSERT OR UPDATE ON deployments
FOR EACH ROW EXECUTE FUNCTION analytics_deployment_completion();
INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused)
 SELECT 'deployment',t.id,a.project_id,a.name,t.state,COALESCE(t.started_at,t.created_at),COALESCE(t.finished_at,t.created_at),0
 FROM deployments t JOIN apps a ON a.id=t.app_id WHERE t.state IN ('succeeded','failed','cancelled');
CREATE FUNCTION analytics_workflow_completion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IN ('succeeded','failed','cancelled') THEN
  IF TG_OP = 'INSERT' THEN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'workflow',NEW.id,c.project_id,r.name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),0 FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=NEW.resource_id;
  ELSIF (OLD.state<>NEW.state OR COALESCE(OLD.finished_at,'')<>COALESCE(NEW.finished_at,'')) THEN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'workflow',NEW.id,c.project_id,r.name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),0 FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=NEW.resource_id;
  END IF;
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER analytics_workflow_completion AFTER INSERT OR UPDATE ON workflow_revisions
FOR EACH ROW EXECUTE FUNCTION analytics_workflow_completion();
INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused)
 SELECT 'workflow',t.id,c.project_id,r.name,t.state,COALESCE(t.started_at,t.created_at),COALESCE(t.finished_at,t.created_at),0
 FROM workflow_revisions t JOIN workflow_resources r ON r.id=t.resource_id JOIN config_sources c ON c.id=r.config_source_id WHERE t.state IN ('succeeded','failed','cancelled');
CREATE FUNCTION analytics_job_completion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IN ('succeeded','failed','cancelled') THEN
  IF TG_OP = 'INSERT' THEN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'job',NEW.id,c.project_id,r.name || ' / ' || NEW.job_name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),CASE WHEN COALESCE(NEW.reused_from_id,'')<>'' THEN 1 ELSE 0 END FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=NEW.resource_id;
  ELSIF (OLD.state<>NEW.state OR COALESCE(OLD.finished_at,'')<>COALESCE(NEW.finished_at,'')) THEN INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused) SELECT 'job',NEW.id,c.project_id,r.name || ' / ' || NEW.job_name,NEW.state,COALESCE(NEW.started_at,NEW.created_at),COALESCE(NEW.finished_at,NEW.created_at),CASE WHEN COALESCE(NEW.reused_from_id,'')<>'' THEN 1 ELSE 0 END FROM workflow_resources r JOIN config_sources c ON c.id=r.config_source_id WHERE r.id=NEW.resource_id;
  END IF;
 END IF;
 RETURN NEW;
END; $$;
CREATE TRIGGER analytics_job_completion AFTER INSERT OR UPDATE ON workflow_job_results
FOR EACH ROW EXECUTE FUNCTION analytics_job_completion();
INSERT INTO analytics_outbox(kind,entity_id,project_id,name,state,started_at,finished_at,reused)
 SELECT 'job',t.id,c.project_id,r.name || ' / ' || t.job_name,t.state,COALESCE(t.started_at,t.created_at),COALESCE(t.finished_at,t.created_at),CASE WHEN COALESCE(t.reused_from_id,'')<>'' THEN 1 ELSE 0 END
 FROM workflow_job_results t JOIN workflow_resources r ON r.id=t.resource_id JOIN config_sources c ON c.id=r.config_source_id WHERE t.state IN ('succeeded','failed','cancelled');
