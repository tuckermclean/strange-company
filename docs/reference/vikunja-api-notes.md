# Vikunja API notes — Kanban board sync

Target: Vikunja **2.5.0** (chart `appVersion`), API served at `/api/v1` (classic
CRUD, echo-based) and `/api/v2` (newer, huma-based, standard REST verbs).

All evidence below was read directly from `github.com/go-vikunja/vikunja`. Every
cited file was fetched **pinned to the `v2.5.0` tag** (`?ref=v2.5.0`) and then
diffed against the `main` branch HEAD; unless a diff is called out explicitly,
the two were byte-identical for the cited code, so what's written here is true
of the exact release you're running. Fetches used:

```
gh api "repos/go-vikunja/vikunja/contents/<PATH>?ref=v2.5.0" --jq '.content' | tr -d '\n' | base64 -d
```

Every claim is marked **VERIFIED** (file + what was read) or **UNVERIFIED**
(best guess + why it couldn't be confirmed by reading source alone). Do not
treat an UNVERIFIED line as fact without testing it against a live instance.

---

## 0. The single most important gotcha

**v1 and v2 use opposite create/update verbs.** This is not a guess — it's
stated in the server's own code, which auto-derives API-token permission
groups from the HTTP method and needs to special-case this exact fact:

```go
// v1 and v2 have inverted create/update verbs: v1 uses PUT for create and POST
// for update, v2 follows REST conventions (POST create, PUT/PATCH update).
func getRouteDetail(route echo.RouteInfo) (method string, detail *RouteDetail) {
    ...
    switch route.Method {
    case http.MethodPut:
        if v2 {
            return "update", detail   // v2: PUT replaces an existing resource → update.
        }
        return "create", detail       // v1: PUT is used for creating resources.
    case http.MethodPost:
        if v2 {
            return "create", detail   // v2: POST creates a new resource on the collection.
        }
        return "update", detail       // v1: POST is used for updating resources.
    case http.MethodPatch:
        return "update", detail       // Both versions use PATCH for partial updates.
    ...
```
VERIFIED — `pkg/models/api_routes.go` (v2.5.0), lines ~110–150.

So: if you build against `/api/v1`, create = `PUT`, update = `POST`. If you
build against `/api/v2`, create = `POST`, update = `PUT` (and `PATCH` also
works for partial updates on v2). Everything below gives both where it matters.

---

## 1. PROJECTS

### v1 routes
VERIFIED — `pkg/routes/routes.go` (v2.5.0), lines ~590–595:
```go
a.GET("/projects", projectHandler.ReadAllWeb)
a.GET("/projects/:project", projectHandler.ReadOneWeb)
a.POST("/projects/:project", projectHandler.UpdateWeb)
a.DELETE("/projects/:project", projectHandler.DeleteWeb)
a.PUT("/projects", projectHandler.CreateWeb)
```
So: `GET /api/v1/projects` (list), `PUT /api/v1/projects` (create), `GET
/api/v1/projects/{id}` (read one), `POST /api/v1/projects/{id}` (update),
`DELETE /api/v1/projects/{id}` (delete). Swagger annotations confirm the same
routes with response codes: `ReadAll` → `200 {array} models.Project` at
`/projects [get]`, `Create` → `201 {object} models.Project` at `/projects
[put]`, `Update` → `200` at `/projects/{id} [post]`, `Delete` → `200
{object} models.Message` at `/projects/{id} [delete]`.
VERIFIED — `pkg/models/project.go` (v2.5.0), swagger comments at lines
~211/392/1418/1469/1501, handler funcs at ~215/396/1423/1474/1506.

### Project JSON fields (relevant subset)
VERIFIED — `pkg/models/project.go` (v2.5.0), `type Project struct` (~line 40):
```go
ID              int64             `json:"id" readOnly:"true"`
Title           string            `json:"title" valid:"required,runelength(1|250)"`
Description     string            `json:"description"`
Identifier      string            `json:"identifier" valid:"runelength(0|10)"`   // used to build task identifiers, e.g. PROJ-123
HexColor        string            `json:"hex_color"`
ParentProjectID *int64            `json:"parent_project_id"`  // 0/omit = unchanged; explicit 0 detaches to top level (needs admin)
Owner           *user.User        `json:"owner" readOnly:"true"`
IsArchived      bool              `json:"is_archived"`
IsFavorite      bool              `json:"is_favorite"`
Position        float64           `json:"position"`
Views           []*ProjectView    `json:"views" readOnly:"true"`   // populated on read; managed via view endpoints
MaxPermission   Permission        `json:"max_permission" readOnly:"true"`  // 0=read,1=read/write,2=admin
Created         time.Time         `json:"created" readOnly:"true"`
Updated         time.Time         `json:"updated" readOnly:"true"`
```
Note `OwnerID` is `json:"-"` (not exposed; only the resolved `owner` object is).

### Project.Create auto-creates the 4 default views (including Kanban)
`Project.Create()` calls `CreateProject()` which calls
`CreateDefaultViewsForProject()`, which unconditionally creates List, Gantt,
Table, and Kanban views for every new project:
```go
kanban := &ProjectView{
    ProjectID:               project.ID,
    Title:                   "Kanban",
    ViewKind:                ProjectViewKindKanban,
    Position:                400,
    BucketConfigurationMode: BucketConfigurationModeManual,
}
```
VERIFIED — `pkg/models/project_view.go` (v2.5.0), `CreateDefaultViewsForProject`
(~line 808) and `Project.Create` in `project.go` (~line 1474, calls
`CreateProject(...)` which at `project.go:1140` calls
`CreateDefaultViewsForProject`). **Consequence: you never need to create a
Kanban view yourself — just create the project and then look up its views.**

### Pagination (applies to projects, tasks, and everything else)
```go
func getLimitFromPageIndex(page int, perPage int) (limit, start int) {
    if page < 1 {
        return 0, 0   // page not set (0) or -1 → limit 0 == "get everything", no pagination
    }
    limit = config.ServiceMaxItemsPerPage.GetInt()  // default 50
    if perPage > 0 {
        limit = perPage
    }
    start = limit * (page - 1)
    return
}
```
VERIFIED — `pkg/models/models.go` (v2.5.0), lines ~88–101; default value
`ServiceMaxItemsPerPage.setDefault(50)` in `pkg/config/config.go` line ~360.
**Important nuance: if you omit `page` entirely (or send 0), Vikunja returns
ALL rows unpaginated — there is no default page size unless you explicitly
pass `page=1`.** Response carries `x-pagination-total-pages` and
`x-pagination-result-count` headers (set in `pkg/web/handler/read_all.go`
lines ~113–115, VERIFIED).

---

## 2. VIEWS and BUCKETS

### Listing/creating views
VERIFIED — `pkg/routes/routes.go` (v2.5.0), lines ~889–893:
```go
a.GET("/projects/:project/views", projectViewProvider.ReadAllWeb)
a.GET("/projects/:project/views/:view", projectViewProvider.ReadOneWeb)
a.PUT("/projects/:project/views", projectViewProvider.CreateWeb)      // v1 create
a.DELETE("/projects/:project/views/:view", projectViewProvider.DeleteWeb)
a.POST("/projects/:project/views/:view", projectViewProvider.UpdateWeb) // v1 update
```

### Identifying the Kanban view
`GET /api/v1/projects/{project}/views` returns an array of `ProjectView`. Find
the one where `view_kind == "kanban"`. Do **not** match on title — the title
("Kanban" by default) is just a user-editable label. `ViewKind` is a typed
enum serialized as one of `list`, `gantt`, `table`, `kanban`.
VERIFIED — `pkg/models/project_view.go` (v2.5.0), `MarshalJSON`/`UnmarshalJSON`
on `ProjectViewKind` (~lines 37–79) map exactly those four strings.

### ProjectView JSON fields (relevant subset)
VERIFIED — `pkg/models/project_view.go` (v2.5.0), `type ProjectView struct`
(~line 149):
```go
ID                      int64                          `json:"id" readOnly:"true"`
Title                   string                         `json:"title"`
ProjectID               int64                          `json:"project_id" readOnly:"true"`
ViewKind                ProjectViewKind                `json:"view_kind"`  // list|gantt|table|kanban
Filter                  *TaskCollection                `json:"filter"`
Position                float64                        `json:"position"`
BucketConfigurationMode BucketConfigurationModeKind    `json:"bucket_configuration_mode"` // none|manual|filter
BucketConfiguration     []*ProjectViewBucketConfiguration `json:"bucket_configuration"`
DefaultBucketID         int64                          `json:"default_bucket_id"`  // where new/unbucketed tasks land
DoneBucketID            int64                          `json:"done_bucket_id"`     // moving a task here marks it done
Updated, Created        time.Time                      `json:"updated"/"created" readOnly:"true"`
```
The default Kanban view is created with `BucketConfigurationMode: manual`
(true drag-and-drop buckets, as opposed to `filter`-computed pseudo-buckets).

### Listing / creating buckets
VERIFIED — `pkg/routes/routes.go` (v2.5.0), lines ~622–625:
```go
a.GET("/projects/:project/views/:view/buckets", kanbanBucketHandler.ReadAllWeb)
a.PUT("/projects/:project/views/:view/buckets", kanbanBucketHandler.CreateWeb)   // v1 create
a.POST("/projects/:project/views/:view/buckets/:bucket", kanbanBucketHandler.UpdateWeb) // v1 update
a.DELETE("/projects/:project/views/:view/buckets/:bucket", kanbanBucketHandler.DeleteWeb)
```
Body for create: `{"title": "In Progress"}` (only `title` is required; the
project/view come from the URL path, not the body).

**Note:** `GET /projects/{id}/views/{view}/buckets` returns bare `Bucket`
objects **without their tasks** — the handler's own doc comment says so:
"Returns all kanban buckets... To get all buckets with their tasks, use the
tasks endpoint with a kanban view." (see §3 below for that endpoint.)
VERIFIED — `pkg/models/kanban.go` (v2.5.0), `Bucket.ReadAll` (~line 99–145).

### Bucket JSON fields
VERIFIED — `pkg/models/kanban.go` (v2.5.0), `type Bucket struct` (~line 30):
```go
ID            int64      `json:"id"`
Title         string     `json:"title" valid:"required"`
ProjectViewID int64      `json:"project_view_id"`   // from URL :view on write
Tasks         []*Task    `json:"tasks,omitempty"`   // only populated via the tasks-in-view endpoint
Limit         int64      `json:"limit" minimum:"0"` // WIP limit, 0 = unlimited
Count         int64      `json:"count"`             // current task count, computed
Position      float64    `json:"position"`
Created, Updated time.Time
CreatedBy     *user.User `json:"created_by"`
```
There is **no `project_id` field on Bucket** in the JSON (it's
`xorm:"-" json:"-" param:"project"` — used only to bind the URL param
internally, never serialized).

---

## 3. TASKS

### Create / read / update / list
VERIFIED — `pkg/routes/routes.go` (v2.5.0), lines ~614–644:
```go
a.GET("/projects/:project/views/:view/tasks", taskCollectionHandler.ReadAllWeb)
a.GET("/projects/:project/tasks", taskCollectionHandler.ReadAllWeb)
a.PUT("/projects/:project/tasks", taskHandler.CreateWeb)             // v1 create
a.GET("/tasks/:projecttask", taskHandler.ReadOneWeb)
a.GET("/projects/:project/tasks/by-index/:index", taskHandler.ReadOneWeb, ResolveProjectIdentifier())
a.DELETE("/tasks/:projecttask", taskHandler.DeleteWeb)
a.POST("/tasks/:projecttask", taskHandler.UpdateWeb)                 // v1 update
```
So: `PUT /api/v1/projects/{project}/tasks` create, `GET /api/v1/tasks/{id}`
read one, `POST /api/v1/tasks/{id}` update, `GET
/api/v1/projects/{project}/tasks` or `.../views/{view}/tasks` list.

### Task JSON fields (relevant subset)
VERIFIED — `pkg/models/tasks.go` (v2.5.0), `type Task struct` (~line 60):
```go
ID          int64     `json:"id" readOnly:"true"`
Title       string    `json:"title" valid:"minstringlength(1)"`
Description string    `json:"description"`
Done        bool      `json:"done"`
DoneAt      time.Time `json:"done_at" readOnly:"true"`
DueDate     time.Time `json:"due_date"`
ProjectID   int64     `json:"project_id"`  // on create: from URL; on update, changing it MOVES the task
Identifier  string    `json:"identifier" readOnly:"true"`  // e.g. "PROJ-12", derived server-side
Index       int64     `json:"index" readOnly:"true"`       // per-project sequence number, server-assigned
BucketID    int64     `json:"bucket_id"`   // see caveat below — NOT reliably populated, and writing it is a no-op
Buckets     []*Bucket `json:"buckets,omitempty"` // only populated when ?expand=buckets is used
Position    float64   `json:"position" readOnly:"true"`  // per-view; 0 unless fetched through a view endpoint
Created     time.Time `json:"created" readOnly:"true"`
Updated     time.Time `json:"updated" readOnly:"true"`
```

### Pagination for task listing
Same `getLimitFromPageIndex` helper as projects (default 50/page if `page>=1`
is passed, unpaginated if omitted). VERIFIED — `pkg/models/task_search.go`
(v2.5.0), line ~561 calls `getLimitFromPageIndex(opts.page, opts.perPage)`
then `.Limit(limit, start)` at line ~605.

### ★ How a task moves between Kanban buckets — the important one

**There is a dedicated relation endpoint, and it is the only reliable way to
move a task between buckets.** Route:

- v1: `POST /api/v1/projects/{project}/views/{view}/buckets/{bucket}/tasks`
- v2: `PUT  /api/v2/projects/{project}/views/{view}/buckets/{bucket}/tasks`

VERIFIED (v1) — `pkg/routes/routes.go` (v2.5.0), line ~901:
```go
a.POST("/projects/:project/views/:view/buckets/:bucket/tasks", taskBucketProvider.UpdateWeb)
```
VERIFIED (v2) — `pkg/routes/api/v2/task_bucket.go` (v2.5.0), full file:
```go
Register(api, huma.Operation{
    OperationID: "task-bucket-update",
    Summary:     "Place a task in a kanban bucket",
    Method: http.MethodPut,
    Path:   "/projects/{project}/views/{view}/buckets/{bucket}/tasks",
}, taskBucketUpdate)
```
with comment: *"Idempotent: re-sending the same bucket is a no-op. ... Moving
a task into a bucket that is already at its task limit is rejected with 412."*

**Payload:** `TaskBucket` object — you send `{"task_id": <id>}` in the body;
`bucket_id`/`project_view_id`/`project_id` are all taken from the URL path
and any value you put in the body for them is ignored (`param:"bucket"` /
`param:"view"` / `param:"project"` tags). VERIFIED —
`pkg/models/kanban_task_bucket.go` (v2.5.0), `type TaskBucket struct`
(lines 33–47), doc tag on `BucketID`: *"On /api/v2 this is taken from the
URL; a value in the body is ignored."*

**Response:** the updated `TaskBucket`, which includes the resolved `bucket`
(full `Bucket` object with updated `count`) and `task` (the task after the
move, reflecting any done-state change) — VERIFIED, same struct, `Bucket
*Bucket` / `Task *Task` fields with `readOnly:"true"` doc tags.

**Side effects, all VERIFIED from `updateTaskBucket()` in
`kanban_task_bucket.go` (v2.5.0, lines 88–231):**
- Moving into the view's `done_bucket_id` marks the task done (`done_at` set);
  moving out of it un-marks it done.
- A repeating task moved into the done bucket is immediately reopened and
  routed to the view's `default_bucket_id` instead (so the next recurrence
  shows up in the default column, not "done").
- When a task's done-state flips as a side effect, it is also pushed into the
  done bucket of every *other* Kanban view on the same project that has a
  `done_bucket_id` configured.
- Moving a task into a bucket already at its WIP `limit` fails (v2: HTTP 412).
- Re-sending the same `bucket_id` the task is already in is a harmless no-op
  (checked by comparing old vs. new bucket id before doing anything).
- Fires a `task.updated` webhook event (see §4) — **there is no separate
  "task moved" or "bucket changed" event.**

**A subtlety worth flagging: `task.bucket_id` in the Task JSON is NOT a
reliable way to move a task, despite its doc comment saying it "can be used
to move a task between buckets."** Tracing `Task.updateSingleTask()` (the
`POST /tasks/{id}` handler): `bucket_id` is listed in `colsToUpdate` and
passed to `s.ID(t.ID).Cols(colsToUpdate...).Update(&ot)`, but `Task.BucketID`
is tagged `xorm:"-"` — it is **not a real database column** (bucket
membership lives in a separate `task_buckets` join table, updated only via
`TaskBucket.upsert()`/`updateTaskBucket()`). Since xorm has no column to map
`"bucket_id"` to, that `Cols()` entry is inert for a plain task update; no
code path in `updateSingleTask` calls `updateTaskBucket()` unless the
**project** changed (`t.ProjectID != ot.ProjectID`) or the **done** state
changed. **Conclusion: use the dedicated `.../buckets/{bucket}/tasks`
endpoint to move a task between buckets within the same view/project; do not
rely on `PATCH`/`POST`ing `bucket_id` on the task itself.** VERIFIED —
`pkg/models/tasks.go` (v2.5.0), `updateSingleTask` lines ~1256–1600 (esp.
1349–1351 for the no-op `bucket_id` cols entry, and 1363–1447 for the only
two paths that actually call `updateTaskBucket`).

### How to read which bucket a task is currently in

This is asymmetric depending on which endpoint you use — all VERIFIED by
tracing the code:

1. **`GET /projects/{project}/views/{view}/tasks` on a Kanban view** (manual
   or filter mode) returns an **array of `Bucket` objects**, each with a
   nested `tasks` array — not a flat task list — and each task in that array
   has `bucket_id` set correctly. This is because
   `getTaskOrTasksInBuckets()` routes to `GetTasksInBucketsForView()`, which
   explicitly does `for _, t := range ts { t.BucketID = bucket.ID }` before
   assembling the response (`pkg/models/kanban.go`, ~line 261). Passing a
   `bucket_id` filter, or calling the **v2** task-collection endpoint (which
   sets `forceFlatTasks`), instead returns a flat `[]Task` with `bucket_id`
   populated per task. VERIFIED — `pkg/models/task_collection.go` (v2.5.0),
   `getTaskOrTasksInBuckets()` lines ~164–182, and
   `TaskCollection.SetForceFlatTasks()` doc comment: *"The v2 tasks endpoint
   uses it; v1 leaves it unset for the polymorphic shape."*
2. **`GET /tasks/{id}` (single task) does NOT populate `bucket_id`** by
   default — it will read back as `0`. `Task.ReadOne()` calls
   `addMoreInfoToTasks(s, taskMap, a, nil, expand)` with a **nil view**, and
   the singular `BucketID` field is only ever set inside
   `GetTasksInBucketsForView` (which needs a view) — never inside the
   generic `addMoreInfoToTasks` path used for single-task reads. To get
   current bucket info for a single task, pass `?expand=buckets`, which
   populates the plural `task.buckets[]` array (all buckets, across all of
   the project's Kanban views, that this task currently sits in — usually
   one entry, more if the project has multiple Kanban views). VERIFIED —
   `pkg/models/tasks.go` (v2.5.0) `Task.ReadOne` (~line 2225) and
   `addBucketsToTasks()` (~line 640).

---

## 4. WEBHOOKS

### Creating a webhook
VERIFIED — `pkg/routes/routes.go` (v2.5.0), lines ~866–870 (gated behind
`config.WebhooksEnabled`):
```go
a.GET("/projects/:project/webhooks", webhookProvider.ReadAllWeb)
a.PUT("/projects/:project/webhooks", webhookProvider.CreateWeb)   // v1 create
a.DELETE("/projects/:project/webhooks/:webhook", webhookProvider.DeleteWeb)
a.POST("/projects/:project/webhooks/:webhook", webhookProvider.UpdateWeb) // v1 update — events only
a.GET("/webhooks/events", apiv1.GetAvailableWebhookEvents)
```
`GET /api/v1/webhooks/events` returns the live list of event name strings
your server build supports (see below for the ones relevant to tasks).

### Webhook JSON fields
VERIFIED — `pkg/models/webhooks.go` (v2.5.0), `type Webhook struct`
(~line 52):
```go
ID         int64    `json:"id" readOnly:"true"`
TargetURL  string   `json:"target_url" valid:"required,url"`
Events     []string `json:"events" valid:"required"`  // must be from /webhooks/events
ProjectID  int64    `json:"project_id" readOnly:"true"` // from URL, mutually exclusive with user_id
UserID     int64    `json:"user_id" readOnly:"true"`    // user-level webhook (only user-directed events)
Secret     string   `json:"secret" writeOnly:"true"`     // HMAC key; never echoed back
BasicAuthUser, BasicAuthPassword string `writeOnly:"true"`
CreatedBy  *user.User `json:"created_by" readOnly:"true"`
Created, Updated time.Time
```
`Update()` only lets you change `events` — VERIFIED, comment: "You cannot
change other values of a webhook" (`webhooks.go` ~line 285).

### Events relevant to a Kanban sync
`RegisterEventForWebhook` / `RegisterUserDirectedEventForWebhook` calls in
`pkg/models/listeners.go` (v2.5.0, lines 63–81) register exactly this set as
webhook-eligible:
```
task.created, task.updated, task.deleted,
task.assignee.created, task.assignee.deleted,
task.comment.created, task.comment.edited, task.comment.deleted,
task.attachment.created, task.attachment.deleted,
task.relation.created, task.relation.deleted,
project.updated, project.deleted,
project.shared.user, project.shared.team,
task.reminder.fired, task.overdue, tasks.overdue   (user-directed only)
```
Exact event-name strings VERIFIED from each event's `Name()` method in
`pkg/models/events.go` (v2.5.0, lines ~28–224). **Note the Go type
`TaskCommentUpdatedEvent` fires under the string `"task.comment.edited"`,
not `"task.comment.updated"` — use the string, not the type name.**

**There is no dedicated bucket-move event.** A Kanban drag-and-drop is
delivered to webhooks as an ordinary `task.updated` event — the
`TaskBucket.Update()` handler dispatches `TaskUpdatedEvent{Task: b.Task,
Doer: ...}` after the move (`pkg/models/kanban_task_bucket.go`, v2.5.0, lines
248–260), and that's the same event type fired for any other task edit.

### ★ How the target bucket is identified in the delivered payload
The wire payload shape is:
```go
type WebhookPayload struct {
    EventName string      `json:"event_name"`
    Time      time.Time   `json:"time"`
    Data      interface{} `json:"data"`   // == map with "task" and "doer" keys for task.updated
}
```
VERIFIED — `pkg/models/listeners.go` (v2.5.0), `type WebhookPayload struct`
(~line 1156) and `TaskUpdatedEvent{Task, Doer}` in `events.go` (~line 50).

Before delivery, `WebhookListener.Handle()` calls `reloadEventData()` →
`reloadTaskInEvent()`, which **re-fetches the task with `?expand=buckets`
forced on**:
```go
fullTask := Task{
    ID: id,
    Expand: []TaskCollectionExpandable{TaskCollectionExpandBuckets},
}
err := fullTask.ReadOne(s, &user.User{ID: doerID})
...
event["task"] = fullTask
```
VERIFIED — `pkg/models/listeners.go` (v2.5.0), `reloadTaskInEvent()`
(~lines 1315–1352).

**Consequence: the delivered `data.task.bucket_id` scalar field will be `0`
(unpopulated) — the same singular-field gap described in §3.2 applies here
too, because this reload is a plain `ReadOne`.** The buckets the task landed
in are instead only available as `data.task.buckets[]`, an array of full
`Bucket` objects (id, title, `project_view_id`, position, limit, count) — one
entry per Kanban view the task belongs to. **To find "which bucket did this
webhook move the task into," match `task.buckets[].project_view_id` against
the Kanban view you care about; do not read `task.bucket_id`.** This is
important and easy to get wrong when building a consumer.

### Delivery retry — REFUTING "one-shot, no retry"
This is **not** one-shot. Delivery goes through watermill's retry middleware
with exponential backoff, and only gives up after parking the message in a
poison queue:
```go
router.AddMiddleware(
    handlerTracker,
    poison,
    middleware.Retry{
        MaxRetries:          5,
        InitialInterval:     time.Millisecond * 100,
        MaxInterval:         time.Hour,
        Multiplier:          2,
        MaxElapsedTime:      0,
        RandomizationFactor: 1,
        Logger:              logger,
    }.Middleware,
    middleware.Recoverer,
)
```
VERIFIED — `pkg/events/events.go` (v2.5.0), lines ~117–129. The delivery
consumer's own doc comment confirms the intent explicitly:
```go
// WebhookDeliveryListener delivers one webhook per message. It is the
// consumer for WebhookDeliveryEvent and owns the retry semantics: any
// error returned from Handle triggers the watermill retry middleware
// independently for this single delivery ... and eventually
// parks it in the poison queue if all retries fail.
```
VERIFIED — `pkg/models/listeners.go` (v2.5.0), lines ~1163–1187.
`sendWebhookPayload()` returns an error for any non-2xx response or transport
failure (`pkg/models/webhooks.go`, ~lines 350–390), which is exactly what
triggers a retry.

**Caveat worth flagging for reliability planning:** the pub/sub backing this
is an **in-process, in-memory** `gochannel` (`pubsub =
gochannel.NewGoChannel(...)`, `pkg/events/events.go` line ~81) — retries are
not persisted to durable storage. **If the Vikunja process restarts or
crashes while a delivery is mid-backoff, that pending retry (and the poison
queue) is lost.** So: retried, yes; durable across restarts, no. Treat
webhook delivery as "at-least-once, best-effort" rather than "exactly-once,
guaranteed" — a sync tool should still reconcile periodically via the read
APIs rather than trusting webhooks as the sole source of truth.

### Signature / HMAC
```go
if len(w.Secret) > 0 {
    sig256 := hmac.New(sha256.New, []byte(w.Secret))
    sig256.Write(payload)
    signature := hex.EncodeToString(sig256.Sum(nil))
    req.Header.Add("X-Vikunja-Signature", signature)
}
```
VERIFIED — `pkg/models/webhooks.go` (v2.5.0), `sendWebhookPayload()`
(~lines 350–365). **Algorithm: HMAC-SHA256 over the raw JSON request body,
hex-encoded (lowercase hex, not base64), header `X-Vikunja-Signature`.**
Signing only happens if you set a `secret` when creating/updating the
webhook — the field is `writeOnly`, so you can't read it back to verify it
was actually stored, only infer it from whether signed requests arrive.
Also: `User-Agent: Vikunja/<version>` and `Content-Type: application/json`
are always set; Basic Auth is added instead of/alongside HMAC if
`basic_auth_user`/`basic_auth_password` were set.

---

## 5. AUTH

### Header format
Bearer auth for both JWT and API tokens: `Authorization: Bearer <token>`.
VERIFIED — `pkg/routes/routes.go` (v2.5.0) swagger doc comment (~lines
31–33): *"API Token: ... The token must be provided via an `Authorization:
Bearer <token>` header, similar to jwt auth."*

### Do API tokens cover projects/views/buckets/tasks/webhooks?
Yes, mechanically — all of these are registered through the same
`handler.WebHandler` CRUD wiring that `collectRoutesForAPITokens()` walks at
startup to build the permission-group table (`pkg/routes/routes.go`
~line 295: `collectRoutesForAPITokens(e)`), and `CanDoAPIRoute()` checks a
token's stored `(path, method)` pairs generically — there's no special-case
that excludes any of these resources. VERIFIED (mechanism) —
`pkg/models/api_routes.go` (v2.5.0), `CanDoAPIRoute()` (~lines 440–465).

Permission group **names** are derived deterministically from the URL path
with parameters stripped, snake_cased, joined by `_` (`getRouteGroupName()`,
`pkg/models/api_routes.go` ~lines 82–100), with two special-cased
collapses (`projects_tasks`/`tasks_all` → `tasks`,
`projects_tasks_bulk` → `tasks_bulk`). Applying that algorithm by hand to the
routes above gives:
- `projects` — project CRUD
- `projects_views` — view CRUD (`/projects/:project/views...`)
- `projects_views_buckets` — bucket CRUD (`/projects/:project/views/:view/buckets...`)
- `tasks` — task CRUD and both task-listing routes (collapsed by the special case)
- `projects_webhooks` — webhook CRUD

**UNVERIFIED (derivation, not observation):** I traced the naming algorithm
in source but did not run a live server to fetch `GET /api/v1/routes` and
confirm these exact strings appear verbatim (in particular whether
`projects_views_buckets` gets any further special-casing I missed, e.g. via
the `isStandardCRUDRoute`/`crudResources` table which separately lists
`"projects_buckets"` — a name that doesn't match what the join algorithm
produces for the current bucket route, suggesting either a legacy/unused
label or a route I didn't check). **Before wiring a token's
`permissions` JSON, call `GET /api/v1/routes` (or `/api/v2/routes` — both
are exposed per `a.GET("/routes", models.GetAvailableAPIRoutesForToken)` at
`routes.go` line ~512) against your actual instance and read back the exact
group keys it returns, rather than trusting the derivation above verbatim.**

An API token's permission JSON shape (needed for the above): `{"tasks":
["read_all","update"]}` — group name → array of permission names among
`read_all`, `read_one`, `create`, `update`, `delete`. VERIFIED —
`pkg/models/api_tokens.go` (v2.5.0), `APIToken.APIPermissions` doc comment.

### POST /login — non-existent user vs. wrong password

**These are indistinguishable.** Both return the exact same response:
**HTTP 403**, body `{"code": 1011, "message": "Wrong username or
password."}`. The code deliberately disguises which case occurred by still
hashing something even when the username lookup fails, specifically to
avoid a timing/response oracle:

```go
func CheckUserCredentials(ctx context.Context, s *xorm.Session, u *Login) (*User, error) {
    if u.Password == "" || u.Username == "" {
        return nil, ErrNoUsernamePassword{}
    }
    user, err := getUserByUsernameOrEmail(s, u.Username)
    if err != nil {
        // hashing the password takes a long time, so we hash something to not make it clear if the username was wrong
        _, _ = bcrypt.GenerateFromPassword([]byte(u.Username), config.ServiceBcryptRounds.GetInt())
        return nil, ErrWrongUsernameOrPassword{}
    }
    ...
    err = CheckUserPassword(user, u.Password)
    if err != nil {
        if IsErrWrongUsernameOrPassword(err) {
            handleFailedPassword(ctx, user)
        }
        return user, err
    }
    ...
}
```
and
```go
func CheckUserPassword(user *User, password string) error {
    err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password))
    if err != nil {
        if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
            return ErrWrongUsernameOrPassword{}
        }
        return err
    }
    return nil
}
```
VERIFIED — `pkg/user/user.go` (v2.5.0), `CheckUserCredentials` (~lines
371–411) and `CheckUserPassword` (~lines 469–479). The error's HTTP mapping:
```go
type ErrWrongUsernameOrPassword struct{}
func (err ErrWrongUsernameOrPassword) Error() string { return "Wrong username or password" }
const ErrCodeWrongUsernameOrPassword = 1011
func (err ErrWrongUsernameOrPassword) HTTPError() web.HTTPError {
    return web.HTTPError{HTTPCode: http.StatusForbidden, Code: ErrCodeWrongUsernameOrPassword, Message: "Wrong username or password."}
}
```
VERIFIED — `pkg/user/error.go` (v2.5.0), lines ~212–226. The call chain from
the actual `POST /login` handler is `Login()` (`pkg/routes/api/v1/login.go`)
→ `shared.AuthenticateUserCredentials()` (`pkg/routes/api/shared/auth.go`) →
`resolveLoginUser()` → `user.CheckUserCredentials()` — no branch anywhere in
that chain distinguishes the two cases in what's returned to the client.

**This matters for a bootstrap script that decides "register vs. use
existing" from the login response: you cannot make that decision from
`POST /login` alone.** A 403 with code 1011 means only "these credentials
don't work," not "this user doesn't exist." (Also note: a bot user attempting
to log in gets a *different*, earlier-short-circuited error,
`ErrAccountIsBot`, thrown from `resolveLoginUser()` before password checking
even happens — see §6.) To actually tell whether a username is taken,
you'd need a different endpoint (e.g. attempt registration and read the
`ErrUsernameExists` response, or use an admin/user-search endpoint if your
token has that permission) — **UNVERIFIED which admin-facing endpoint is
best for this in your deployment; I did not audit the full set of
username-existence-check endpoints beyond noting that registration surfaces
it.**

---

## 6. BOT USERS

There **is** a bot-user API — both v1 and v2 expose it, with the standard
verb inversion between them.

### v1
VERIFIED — `pkg/routes/routes.go` (v2.5.0), lines ~579–583 (inside the `u`
group, which is mounted under `/user`... **UNVERIFIED base-path detail**: I
did not trace the exact prefix `u` resolves to in this excerpt; treat
`/bots` below as relative to whatever `u`'s prefix is and confirm against
`GET /api/v1/routes` or the Scalar docs on your instance):
```go
u.PUT("/bots", botHandler.CreateWeb)
u.GET("/bots", botHandler.ReadAllWeb)
u.GET("/bots/:bot", botHandler.ReadOneWeb)
u.POST("/bots/:bot", botHandler.UpdateWeb)
u.DELETE("/bots/:bot", botHandler.DeleteWeb)
```

### v2 (confirmed full path and verbs)
VERIFIED — `pkg/routes/api/v2/bot_users.go` (v2.5.0), full file:
```go
GET    /user/bots         list bots owned by the caller
GET    /user/bots/{bot}   read one (ETag support)
POST   /user/bots         create   (v2 verb: POST = create)
PUT    /user/bots/{bot}   update (full replace; PATCH also accepted for partial)
DELETE /user/bots/{bot}
```

### Create payload / behavior
VERIFIED — `pkg/user/user_create.go` (v2.5.0), `CreateBotUser()` (~lines
130–170):
- Caller must be an authenticated **non-bot** user (`owner.IsBot()` → error);
  a link share cannot create bots either (checked in `BotUser.Create`,
  `pkg/models/bot_users.go` line ~44: `owner, ok := a.(*user.User)`).
- **Username must start with `bot-`** (`ErrBotUsernameMustHavePrefix` if not).
- Username must not already exist.
- The created bot gets `BotOwnerID = owner.ID`, `Status = StatusActive`,
  `Issuer = IssuerLocal`, and explicitly **empty `Password` and `Email`** —
  a bot has no password and therefore **cannot use `POST /login`** at all
  (see below).

Request body (v2, `POST /user/bots`): `{"username": "bot-myintegration",
"name": "My Integration"}`. Response: the created `BotUser` (embeds the full
`user.User` fields plus a `status` field shadowed back into JSON since the
underlying `user.User.Status` is normally `json:"-"`).
VERIFIED — `pkg/models/bot_users.go` (v2.5.0), `type BotUser struct`
(~lines 31–40).

### Why login rejects bots, and how a bot actually authenticates
```go
existingUser, lookupErr := user.GetUserByUsername(s, login.Username)
if lookupErr == nil && existingUser.IsBot() {
    return nil, &user.ErrAccountIsBot{UserID: existingUser.ID}
}
```
VERIFIED — `pkg/routes/api/shared/auth.go` (v2.5.0), `resolveLoginUser()`
(~lines 139–156) — bots are rejected **before** bcrypt even runs, with a
distinct error type from the generic wrong-credentials case. Since a bot has
no password, it can never call `/login`. Instead, its owner mints an API
token *for* the bot:

```go
// OwnerID: the user ID of the token owner. When creating a token for a bot
// user, set this to the bot's ID; the bot must be owned by the authenticated
// user. If omitted, defaults to the authenticated user.
OwnerID int64 `json:"owner_id,omitempty" query:"owner_id"`
```
via `POST /api/v2/tokens` (create) with `{"title": "...", "permissions":
{...}, "expires_at": "...", "owner_id": <bot's user id>}`, authenticated as
the bot's **owner** (their JWT or their own API token). VERIFIED —
`pkg/models/api_tokens.go` (v2.5.0), `APIToken.OwnerID` field doc, and
`pkg/routes/api/v2/api_tokens.go` (v2.5.0), `RegisterAPITokenRoutes()`
description: *"Creates an api token for the authenticated user, or for a bot
they own when owner_id is set."* The response includes the cleartext
`token` field once (`json:"token,omitempty" readOnly:"true"` — *"Returned
only once ... never readable again"*); that cleartext token is what the bot
then uses as its own `Authorization: Bearer <token>` for all subsequent
calls, scoped by whatever `permissions` you granted it.

This exact flow (create bot → mint token with `owner_id`) is also what
Vikunja's own first-party CLI does — there is a whole reference client for
it in the same repo, `veans/internal/client/{users.go,tokens.go}`, whose
comments spell out the same sequence: *"The caller becomes the bot's owner,
which is what allows them to mint API tokens for the bot via POST /tokens
with owner_id."* (This CLI calls the v2 paths — `POST /user/bots`, `POST
/tokens` — consistent with the v2 route file above.) VERIFIED —
`veans/internal/client/users.go` and `veans/internal/client/tokens.go`
(v2.5.0).

---

## Open questions / unverified

1. **Exact live `/routes` output for view/bucket permission groups**
   (§5) — I derived group names (`projects_views`, `projects_views_buckets`,
   etc.) from the naming algorithm in `pkg/models/api_routes.go`, but did not
   run a live 2.5.0 instance to confirm the literal strings `GET
   /api/v1/routes` returns. There's a loose end in the code itself: the
   `isStandardCRUDRoute` helper's `crudResources` map lists a group called
   `"projects_buckets"` which doesn't match what the join algorithm produces
   for the current `/projects/:project/views/:view/buckets` path
   (`"projects_views_buckets"`) — possibly a stale/legacy label, possibly a
   route I didn't find. **Action: hit `GET /api/v1/routes` on your actual
   instance before hard-coding a token's `permissions` JSON.**

2. **Exact URL prefix for the v1 bot routes** — `pkg/routes/routes.go` shows
   `u.PUT("/bots", ...)` etc. inside a route group variable named `u`; I did
   not trace what prefix that group is mounted under in this excerpt (v2's
   `/user/bots` strongly suggests v1 is also `/user/bots`, but I did not
   independently confirm the v1 group's prefix string).

3. **Which endpoint best answers "does this username already exist"** for a
   bootstrap script, given §5's finding that `/login` can't tell you. I
   noted that registration surfaces `ErrUsernameExists`, but did not audit
   all admin/user-lookup endpoints for a cleaner existence check.

4. **`ProjectViewBucketConfiguration`** (the shape of `bucket_configuration`
   when `bucket_configuration_mode = filter`) was not inspected in detail —
   irrelevant if you only use `manual` mode (the default for new Kanban
   views), but worth reading `pkg/models/project_view.go` directly if you
   ever need filter-computed buckets.

5. Everything else in this document was read from source pinned to the
   `v2.5.0` git tag and cross-diffed against `main` HEAD with no material
   differences in the cited code, so confidence is high — but none of it was
   exercised against a **running** Vikunja server. Treat this as "verified by
   reading the implementation," not "verified by testing the API."
