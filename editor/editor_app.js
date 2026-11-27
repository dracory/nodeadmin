/**
 * editor_app.js - workflow editor (Vue 3 + Drawflow)
 *
 * Loads the workflow list, renders the node canvas via Drawflow,
 * and supports drag-and-drop node creation from the palette.
 *
 * The API is POST-only: parameters travel in the JSON request body.
 * Responses use the {status, data, message} envelope where data is a map.
 */
/**
 * RFC 9562 UUIDv7: 48-bit millisecond timestamp, version and variant
 * bits, random remainder. Matches the backend's uuid.NewV7().
 */
function uuidV7() {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    let ms = Date.now();
    for (let i = 5; i >= 0; i--) {
        bytes[i] = ms & 0xff;
        ms = Math.floor(ms / 256);
    }
    bytes[6] = (bytes[6] & 0x0f) | 0x70;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    const h = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
    return h.slice(0, 8) + '-' + h.slice(8, 12) + '-' + h.slice(12, 16) + '-' +
        h.slice(16, 20) + '-' + h.slice(20);
}

/** Rendered node box size in px, shared by group bounds and membership. */
const NODE_SIZE = { w: 200, h: 90 };

/**
 * Component port marker node types (component_child_flow_tree). These are
 * canvas-only pseudo nodes living inside role:"component" childFlows —
 * they never reach the backend node list. On save each marker becomes
 * one entry of the definition's in[]/out[] port arrays
 * (serializeComponentDefs); on load the arrays are turned back into
 * markers (renderComponentDef). The node's data.portIndex pins the port
 * order so dragging a marker never renumbers instance ports.
 */
const COMPONENT_PORT_TYPES = [
    {
        id: 'componentIn', name: 'Component input',
        description: 'Input port of this component',
        inputs: 0, outputs: 1, color: '#7e8ba0',
        icon: 'sign-in-alt', category: 'Component ports'
    },
    {
        id: 'componentOut', name: 'Component output',
        description: 'Output port of this component',
        inputs: 1, outputs: 0, color: '#7e8ba0',
        icon: 'sign-out-alt', category: 'Component ports'
    }
];

/** True when a Drawflow node class is a component port marker. */
function isComponentPortClass(cls) {
    return cls === 'componentIn' || cls === 'componentOut';
}

function initEditorApp() {
    const { createApp } = Vue;

    const app = createApp({
        data() {
            return {
                editor: null,
                workflows: [],
                selectedWorkflow: null,
                saveStatus: '', // '', 'saving', 'saved', 'error'
                runnerId: null, // runner holding this workflow's lease (multi-runner)
                saveStatusMessage: '',
                newWorkflowName: '',
                createError: '',
                editingNodeId: null,
                editingNodeType: '',
                editingNodeName: '',
                editingNodeDisabled: false, // node.data.d (Node-RED disabled flag)
                editingNodeData: '{}',
                editingNodeDataError: '',
                editingNodeSchema: null, // fetched JSON schema for the editing node's type (null = JSON fallback)
                editingNodeSchemaFields: [], // normalized [{key, title, description, field, required, options}]
                editingNodeForm: {}, // form values keyed by schema property name
                editingNodeFormErrors: {},
                // format:"code" fields: live CodeMirror instances keyed
                // by field key ({} when the vendored bundle failed to
                // load — the bound textarea fallback stays visible).
                codeEditorInst: {},
                codeEditorOk: {},
                // Active tab within the code-field group (Node-RED
                // On Start / On Message / On Stop) — '' picks the first
                // code field in schema order.
                codeActiveTab: '',
                nodeTypeSearch: '',
                // Palette grouping: collapsed state per category name,
                // session-scoped (all groups start expanded — pick noted
                // in the task: remembering state needs no persistence).
                paletteCollapsed: {},
                dirty: false, // unsaved changes on the canvas
                importJson: '',
                importError: '',
                // Child flow import modal (childflow_export_import): pasted
                // child flow envelope merged into this workflow.
                flowImportJson: '',
                flowImportError: '',
                // Component definitions are canvas childFlows now: each
                // type:"component" node of the workflow materializes as a
                // role:"component" child flow (component_child_flow_tree) edited in
                // place with componentIn/componentOut port markers. The
                // componentDefs computed derives the palette entries
                // ("component:<id>" instances) from those childFlows.
                // Flow env (view.env): Node-RED env-var table
                // [{name,type,value}] consulted by env.get for every
                // node in this workflow. Group env lives on the group
                // objects (view.groups[].env); global env is a
                // server-side singleton edited via the same modal.
                flowEnv: [],
                // Shared env-table editor: target is 'flow' | 'global' |
                // 'group' | 'child flow'; groupId is set for group edits,
                // flowKey for component-child flow env edits.
                envEdit: { target: '', groupId: null, flowKey: '', title: '', rows: [] },
                // Group properties modal (group_properties): edit state
                // for the group targeted by a label double-click;
                // members is a resolved read-only [{id, label}] list.
                groupEdit: { id: null, name: '', disabled: false, members: [] },
                // Flow tabs (task 11): each tab maps to a Drawflow child flow.
                // Since the child flow navigator landed, tabs are only the
                // OPEN childFlows (VS Code model) — the sidebar tree lists
                // every materialized child flow and is the source of truth.
                // Empty by default: no selected
                // workflow means no open tabs, and no open tabs means
                // the workspace is replaced by the welcome screen.
                flowTabs: [],
                activeTab: null,
                // Child flow navigator: knownFlows is the last synced set of
                // canvas child flow keys (used to tell reopened vs. brand-new
                // childFlows apart in syncTabsFromEditor); flowTreeVersion
                // is bumped whenever the child flow set/meta changes so the
                // non-reactive Drawflow model re-renders in the tree.
                knownFlows: [],
                flowTreeVersion: 0,
                // Right-click menu on a child flow tree item. The menu shape
                // mirrors the node context menu (Open/Rename/Export…/
                // Disable/Delete).
                flowContextMenu: { show: false, x: 0, y: 0, childFlow: '' },
                // Sidebar file tree (sidebar_file_tree): the workflow
                // list and child flow navigator merged into one Explorer
                // tree. treeGroups is the in-memory copy of
                // view.treeGroups — a flat list of folder objects
                // {id, name, parentId, childFlows:[childFlowId]}; parentId
                // nests folders, child flow membership is by child flow id
                // (renames don't break it). Folders organize ONLY:
                // no deploy/env/disabled semantics.
                // collapsedTreeNodes tracks expanded state by row key
                // (session-only); treeDropKey highlights the drop
                // target during a child flow drag.
                treeGroups: [],
                collapsedTreeNodes: {},
                folderContextMenu: { show: false, x: 0, y: 0, folderId: null },
                treeDropKey: null,
                dragFlowKey: null,
                // Workflow groups (workflow_groups): top-level folders of
                // the Workflows super-group — flat list of
                // {id,name,parentId,workflows:[workflowId]} organizing
                // workflow DOCUMENTS (treeGroups organize childFlows inside
                // a workflow — different concept). Server-persisted via
                // /api/workflow-groups-* under the global-config
                // "workflowGroups" key; purely visual. dragWorkflowId
                // mirrors dragFlowKey for workflow-row drags.
                workflowGroups: [],
                workflowContextMenu: { show: false, x: 0, y: 0, workflowId: null },
                workflowGroupContextMenu: { show: false, x: 0, y: 0, groupId: null },
                dragWorkflowId: null,
                // Undo/redo history (task 09): capped snapshot stack
                undoStack: [],
                redoStack: [],
                historySnapshot: null, // last serialized editor.export()
                historyDebounce: null, // debounce handle for nodeMoved
                suppressHistory: false, // true while renderFlow/undo applies snapshots
                // Node clipboard + context menu (task 09)
                lastSelectedNodeId: null,
                clipboardNode: null,
                clipboardPos: null, // last pasted position for cascading pastes
                contextMenu: { show: false, x: 0, y: 0, nodeId: null },
                // Minimap (task 08)
                minimapNodes: [],
                minimapView: null,
                minimapTimer: null,
                selectedNodeIds: [], // ctrl/meta-click multi-selection
                // Execution state (tasks 05/06: run/stop + debug panel)
                running: false,
                executionId: null,
                pollTimer: null, // status polling interval handle
                lastEventSeq: 0, // last event sequence seen by the poller
                debugOpen: false,
                debugEvents: [], // [{seq, time, node_id, node_name, level, message}]
                nodeTypes: [],
                // Drawflow reassigns node ids on canvas rebuild, so the
                // visible id often differs from the persisted backend id.
                // Maps bridge canvas id <-> backend id (inject fire, status dots).
                drawflowToBackend: {},
                backendToDrawflow: {},
                // Backend ids queued for the next addNode during renderFlow,
                // so a reload keeps the persisted node ids
                pendingNodeIds: [],
                // "component:<id>" type entries built from the raw
                // definition nodes while a workflow renders — inner
                // instances resolve their def's port counts before the
                // def's own markers exist. Cleared when render ends.
                pendingComponentTypes: {},
                // Drag-wire quick-add (task 37): a connection drag that was
                // released on empty canvas. Holds the source port and the
                // drop point in canvas world coordinates while the Add Node
                // modal is open; null means the modal is in plain "add" mode.
                pendingWire: null,
                // Queue nodes: depth badges refreshed by the status poll,
                // plus the click-to-inspect modal state.
                queueInspect: { nodeId: null, items: [], count: 0, hold: false, routes: [], loading: false, error: '' },
                // Run-details modal: per-node execution journal state
                // (context-menu "Run details"), powered by
                // /api/node-run-journal.
                runDetails: { nodeId: null, entries: [], size: 0, loading: false, error: '' },
                // Workflow properties modal (task
                // workflow_properties): name/description ride
                // the workflow document; the rest are flow-level
                // settings persisted under selectedWorkflow.view and
                // merged on save (saveWorkflow preserves unrelated view
                // keys). Empty string means "unset — engine default".
                // onError picks the designated problem queue: '' none,
                // 'auto' the built-in "__problems__" system queue, or a
                // queue node id (checkpoint nodes are never eligible —
                // see problemQueueIDFromView).
                wfProps: { name: '', description: '', onError: '', maxSteps: '', inboxCapacity: '', inboxPolicy: '', inboxDurable: '' },
                wfPropsError: '',
                wfQueueOptions: [] // onError picker options, refreshed each open
            };
        },
        mounted() {
            const container = document.getElementById('drawflow');
            this.editor = new Drawflow(container);
            // Node ids are UUIDv7 instead of Drawflow's counter
            this.editor.useuuid = true;
            this.editor.getUuid = () => this.nextNodeId();
            // floweditor options (task editor_new_options)
            // multiselect: Ctrl/Shift-click additive selection, Shift-drag
            // selection box, group drag, Delete removes the selection,
            // Ctrl+A selects all. 'selectionChanged' below keeps
            // selectedNodeIds in sync for "Group selected".
            this.editor.multiselect = true;
            // Arrowheads on wires, pointing into the input port
            this.editor.connection_arrow = 'end';
            // Wires going left/backward route as a loop around the nodes
            this.editor.curvature_backward = true;
            // Port-count/duplicate/cycle rules run before the wire is
            // created (mouse drops and addConnection), so a rejected wire
            // never reaches connectionCreated/history.
            this.editor.connection_validator = info => this.validateNewConnection(info);
            this.editor.start();
            this.editor.editor_mode = 'edit';
            // Deep link: after the list loads, ?workflow=<id> selects
            // that workflow; back/forward navigates via popstate.
            this.loadWorkflows().then(() => this.applyWorkflowParam());
            this.loadNodeTypes();
            this._popHandler = () => this.onPopState();
            window.addEventListener('popstate', this._popHandler);

            // Deep link from the Settings page (/editor?env=global):
            // the global env table has a single editor — this modal —
            // so the settings link auto-opens it on arrival.
            if (new URLSearchParams(window.location.search).get('env') === 'global') {
                this.openGlobalEnv();
            }

            // Connection validation + dirty tracking via Drawflow events
            this.editor.on('connectionCreated', info => this.onConnectionCreated(info));
            // Drag-wire quick-add (task 37): floweditor emits connectionDrop
            // when a wire is released off a valid port, carrying the source
            // port and the drop point already in canvas world coordinates.
            this.editor.on('connectionDrop', info => this.onConnectionDrop(info));
            // Closing the Add Node modal without picking a type discards
            // the pending wire (Drawflow already removed the visual path)
            this.$refs.addNodeModal.addEventListener('hidden.bs.modal', () => {
                this.pendingWire = null;
            });
            this.editor.on('nodeCreated', id => { this.refreshOutputLabels(id); this.dirty = true; });
            this.editor.on('import', () => { this.refreshAllOutputLabels(); this.refreshDisabledDom(); this.dirty = true; });
            // Component port markers are nodes — adding/removing one
            // changes a definition's port count, so recompute the
            // child flow-derived palette entries (componentDefs computed
            // keys off flowTreeVersion).
            ['nodeCreated', 'nodeRemoved', 'import']
                .forEach(ev => this.editor.on(ev, () => {
                    if (!this.suppressHistory) this.flowTreeVersion++;
                }));
            // Node groups live in the editor's per-child flow model
            // (floweditor >= v0.2.0): export/import carry them and
            // removeNode/drop-time membership are enforced by the
            // library. App policy: properties modal, dirty, history.
            this.editor.on('groupLabelDblclick', g => this.openGroupProps(g));
            this.editor.on('groupMembershipChanged', () => {
                this.dirty = true;
                this.refreshDisabledDom();
                this.pushHistoryDebounced();
            });
            ['groupCreated', 'groupRemoved', 'groupUpdated', 'groupMoved', 'groupResized']
                .forEach(ev => this.editor.on(ev, () => {
                    this.dirty = true;
                    // Drop-time membership (task 38): dragging the last
                    // member out of a group fires groupRemoved mid
                    // drag-end, while groupMembershipChanged/nodeMoved
                    // already have a history debounce pending. Let it
                    // fold the removal into that single undo entry
                    // instead of pushing a second no-op snapshot.
                    if (ev === 'groupRemoved' && this.historyDebounce) return;
                    this.pushHistory();
                }));
            ['nodeRemoved', 'nodeMoved', 'connectionRemoved', 'nodeDataChanged', 'translate', 'zoom']
                .forEach(ev => this.editor.on(ev, () => { this.dirty = true; }));

            // History snapshots (task 09): record meaningful mutations.
            // connectionCreated pushes only after validation (see onConnectionCreated).
            this.editor.on('nodeCreated', () => this.pushHistory());
            this.editor.on('nodeRemoved', () => this.pushHistory());
            this.editor.on('connectionRemoved', () => this.pushHistory());
            this.editor.on('nodeDataChanged', () => this.pushHistory());
            this.editor.on('nodeMoved', () => this.pushHistoryDebounced());
            this.historySnapshot = this.snapshotNow();

            // Selection tracking for copy/paste/duplicate
            this.editor.on('nodeSelected', id => { this.lastSelectedNodeId = id; });
            this.editor.on('nodeUnselected', () => { this.lastSelectedNodeId = null; });
            // floweditor multiselect mirrors into selectedNodeIds (the
            // list "Group selected" consumes)
            this.editor.on('selectionChanged', ids => {
                this.selectedNodeIds = (ids || []).map(String);
            });

            // Minimap refresh (task 08), debounced
            ['nodeCreated', 'nodeRemoved', 'nodeMoved', 'connectionCreated',
             'connectionRemoved', 'import', 'translate', 'zoom']
                .forEach(ev => this.editor.on(ev, () => this.scheduleMinimap()));

            // Keyboard shortcuts: Ctrl+Z/Y/C/V/D (task 09)
            this._keyHandler = e => this.onGlobalKeydown(e);
            document.addEventListener('keydown', this._keyHandler);

            // Node context menu (right-click) + dismiss on any click
            container.addEventListener('contextmenu', e => this.onCanvasContextMenu(e));
            document.addEventListener('click', () => {
                this.contextMenu.show = false;
                this.flowContextMenu.show = false;
                this.folderContextMenu.show = false;
                this.workflowContextMenu.show = false;
                this.workflowGroupContextMenu.show = false;
            });

            // floweditor v0.6.0: child flow meta (label/role/enabled/env)
            // changes refresh the navigator tree.
            this.editor.on('moduleMetaChanged', () => { this.flowTreeVersion++; });

            // Open node properties on click (not on drag or port clicks)
            let mouseDownPos = null;
            container.addEventListener('mousedown', e => {
                mouseDownPos = { x: e.clientX, y: e.clientY };
            });
            container.addEventListener('click', e => {
                const nodeEl = e.target.closest('.drawflow-node');
                if (!nodeEl || e.target.closest('.input, .output, .drawflow_delete')) return;
                // Inject node fire button: manual trigger, don't open properties
                if (e.target.closest('.inject-fire-btn')) {
                    this.fireInject(nodeEl.id.replace('node-', ''));
                    return;
                }
                // Queue inspect button: opens the queue panel, not properties
                if (e.target.closest('.queue-inspect-btn')) {
                    this.openQueueInspect(nodeEl.id.replace('node-', ''));
                    return;
                }
                const moved = Math.abs(e.clientX - mouseDownPos.x) + Math.abs(e.clientY - mouseDownPos.y);
                if (moved > 4) return;
                const dfId = nodeEl.id.replace('node-', '');
                // Ctrl/meta/shift-click toggles multi-selection for
                // grouping instead of opening the properties panel. With
                // editor.multiselect the library already toggled the node
                // on mousedown — toggling again here would undo it.
                if (e.ctrlKey || e.metaKey || e.shiftKey) {
                    if (!this.editor.multiselect && (e.ctrlKey || e.metaKey)) {
                        this.toggleNodeSelection(dfId);
                    }
                    return;
                }
                if (this.selectedNodeIds.length) this.clearNodeSelection();
                this.openNodeProperties(dfId);
            });
        },
        computed: {
            /**
             * Modal title for the properties dialog — Node-RED style
             * "Edit <Type> Node" in consistent title case: every word
             * of the registered type's name (or the raw type id)
             * capitalized, so mixed-case names still read as a title.
             */
            editingNodeDisplayName() {
                const t = this.findType(this.editingNodeType);
                const raw = (t && t.name) || this.editingNodeType || 'node';
                return raw.replace(/\w+/g, w => w.charAt(0).toUpperCase() + w.slice(1));
            },

            /**
             * format:"code" fields in the current schema, in schema
             * order — the code editor's tab list (Node-RED On Start /
             * On Message / On Stop). One field means no tab bar.
             */
            codeFields() {
                return this.editingNodeSchemaFields
                    .filter(f => f.field === 'code')
                    .sort((a, b) => (a.codeTabOrder || 0) - (b.codeTabOrder || 0));
            },

            /**
             * Node types filtered by the palette search box (name/description)
             */
            filteredNodeTypes() {
                const q = (this.nodeTypeSearch || '').trim().toLowerCase();
                // Palette = core node types + one entry per component
                // definition (placed as a "component:<id>" instance).
                let all = this.nodeTypes.concat(this.componentTypes);
                const inComponent = this.activeFlowRole === 'component';
                if (inComponent) {
                    // Port markers are only placeable inside a
                    // definition child flow; a definition must not
                    // instantiate itself (recursive expansion fails
                    // deploy).
                    const selfId = 'component:' + this.flowIdFor(this.activeTab);
                    all = all.filter(t => t.id !== selfId).concat(COMPONENT_PORT_TYPES);
                }
                // Quick-add (task 37 + input-side drags): a wire dragged
                // from an output needs a type with inputs; a wire dragged
                // from an input needs a type with outputs
                const base = this.pendingWire
                    ? all.filter(t => this.pendingWire.direction === 'input' ? t.outputs > 0 : t.inputs > 0)
                    : all;
                if (!q) return base;
                return base.filter(t =>
                    (t.name || '').toLowerCase().includes(q) ||
                    (t.description || '').toLowerCase().includes(q) ||
                    (t.id || '').toLowerCase().includes(q)
                );
            },

            /**
             * Node-RED-style palette grouping: filteredNodeTypes split
             * into category sections for the Add Node modal. Known
             * categories order first (Node-RED-ish), the rest
             * alphabetical, uncategorized last as "other". Groups with
             * no surviving items are dropped — filtering keeps headers
             * only where matches exist. Component defs and component port
             * markers are just more categories.
             */
            groupedNodeTypes() {
                const order = [
                    'Common', 'Function', 'Actions', 'Network', 'Sequence',
                    'Parsers', 'Storage', 'Dashboard', 'Triggers', 'Queue',
                    'Document', 'Flow Control', 'AI', 'Components', 'Component ports'
                ];
                const buckets = {};
                this.filteredNodeTypes.forEach(t => {
                    const cat = (t.category || '').trim() || 'other';
                    (buckets[cat] = buckets[cat] || []).push(t);
                });
                const rank = c => {
                    const i = order.indexOf(c);
                    if (c === 'other') return order.length + 1;
                    return i === -1 ? order.length : i;
                };
                return Object.keys(buckets)
                    .sort((a, b) => (rank(a) - rank(b)) || a.localeCompare(b))
                    .map(name => ({
                        name: name,
                        items: buckets[name],
                        collapsed: !!this.paletteCollapsed[name]
                    }));
            },

            /**
             * One palette entry per component definition; placing it drops
             * a "component:<defId>" instance node on the canvas. Ports come
             * from the definition's in/out arrays.
             */
            componentTypes() {
                return (this.componentDefs || []).map(d => {
                    const data = d.data || {};
                    const ins = Array.isArray(data.in) ? data.in.length : 0;
                    const outs = Array.isArray(data.out) ? data.out.length : 0;
                    return {
                        id: 'component:' + d.id,
                        name: 'component: ' + (data.name || d.id),
                        description: 'Instance of component "' + (data.name || d.id) + '"',
                        icon: 'object-group',
                        category: 'Components',
                        inputs: ins,
                        outputs: outs,
                        color: '#7e8ba0'
                    };
                });
            },

            /**
             * Groups across all childFlows in the persisted view.groups
             * shape (flat array with a child flow field). Read-only view
             * over the editor's per-child flow model.
             */
            groups() {
                return this.serializeGroups();
            },

            /**
             * Child flow navigator tree: every materialized child flow (open in a
             * tab or not), split into the Flows and Components groups the
             * sidebar renders. Drawflow's child flow model is not reactive —
             * flowTreeVersion is bumped by every mutation path
             * (render/add/delete/rename/meta change) to recompute this.
             */
            flowTree() {
                void this.flowTreeVersion; // reactivity dependency
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const items = Object.keys(df).map(name => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(name) || {}) : {};
                    return {
                        childFlow: name,
                        id: meta.id || name,
                        label: this.flowLabelFor(name),
                        role: meta.role || 'flow',
                        enabled: meta.enabled !== false,
                        open: this.flowTabs.some(t => t.childFlow === name)
                    };
                });
                return {
                    flows: items.filter(m => m.role !== 'component'),
                    components: items.filter(m => m.role === 'component')
                };
            },

            /** Canvas-name lookup over the flat child flow list. */
            flowItemsByName() {
                const map = {};
                this.flowTree.flows.concat(this.flowTree.components)
                    .forEach(m => { map[m.childFlow] = m; });
                return map;
            },

            /**
             * Sidebar super-groups (sidebar_super_groups): the Explorer
             * is split into two fixed UI sections — "Workflows" and
             * "Components" — like VSCode's split Explorer panes (e.g.
             * Open Editors / Files). Section membership is by kind,
             * not by folder: role:"component" childFlows always render
             * under Components even when a folder claims them (folders
             * stay organization-only and only group flow childFlows).
             *
             * workflowTreeRows keeps the sidebar_file_tree arrangement
             * for the Workflows section: root rows are workflow groups
             * (workflowGroups, nested via parentId) and ungrouped
             * workflows — a workflow renders inside its group, the rest
             * form the tail. Only the SELECTED workflow expands into
             * children, because child flow leaves live inside the loaded
             * workflow document (other workflows' childFlows only exist
             * after selectWorkflow fetches them). Children of the
             * selected workflow are user folders (view.treeGroups,
             * nested via parentId) followed by ungrouped flow-child flow
             * leaves; childFlows inside a folder render under it. Stale
             * group/folder refs and duplicate membership (first group
             * wins) degrade gracefully to the ungrouped flat list.
             */
            workflowTreeRows() {
                void this.flowTreeVersion; // reactivity dependency
                const rows = [];
                const items = this.flowItemsByName;
                const emitChildren = (depth) => {
                    // Child flow canvas names claimed by any folder — kept
                    // out of the ungrouped tail below.
                    const claimed = {};
                    this.treeGroups.forEach(g => (g.childFlows || []).forEach(mid => {
                        claimed[this.resolveFlowName(String(mid))] = true;
                    }));
                    const emitFlow = (m, depth) => rows.push({
                        key: 'flow:' + m.childFlow, type: 'flow',
                        item: m, label: m.label, depth: depth
                    });
                    const emitted = {};
                    const seenFolders = {};
                    const emitFolders = (parentId, depth) => {
                        this.treeGroups
                            .filter(g => (g.parentId || null) === parentId && !seenFolders[g.id])
                            .forEach(g => {
                                seenFolders[g.id] = true; // parentId-cycle guard
                                const gkey = 'fg:' + g.id;
                                const open = !this.collapsedTreeNodes[gkey];
                                rows.push({
                                    key: gkey, type: 'folder', folder: g,
                                    label: g.name, depth: depth, expanded: open
                                });
                                if (!open) return;
                                (g.childFlows || []).forEach(mid => {
                                    const m = items[this.resolveFlowName(String(mid))];
                                    // Kind wins over folder membership:
                                    // component childFlows render under the
                                    // Components super-group instead.
                                    if (!m || m.role === 'component') return;
                                    // First folder wins on duplicate
                                    // membership — never emit twice.
                                    if (!emitted[m.childFlow]) {
                                        emitted[m.childFlow] = true;
                                        emitFlow(m, depth + 1);
                                    }
                                });
                                emitFolders(g.id, depth + 1);
                            });
                    };
                    emitFolders(null, depth);
                    this.flowTree.flows
                        .filter(m => !claimed[m.childFlow])
                        .forEach(m => emitFlow(m, depth));
                };
                const emitWorkflow = (wf, depth) => {
                    const selected = !!(this.selectedWorkflow &&
                        this.selectedWorkflow.id === wf.id);
                    const wkey = 'wf:' + wf.id;
                    const expanded = selected && !this.collapsedTreeNodes[wkey];
                    rows.push({
                        key: wkey, type: 'workflow', workflow: wf,
                        label: wf.name, depth: depth,
                        selected: selected, expanded: expanded
                    });
                    if (expanded) emitChildren(depth + 1);
                };
                // First group wins on duplicate membership; a workflow
                // id claimed by any group stays out of the tail.
                const owner = {};
                this.workflowGroups.forEach(g => (g.workflows || []).forEach(wid => {
                    const k = String(wid);
                    if (!owner[k]) owner[k] = g.id;
                }));
                const seenGroups = {};
                const emitGroups = (parentId, depth) => {
                    this.workflowGroups
                        .filter(g => (g.parentId || null) === parentId && !seenGroups[g.id])
                        .forEach(g => {
                            seenGroups[g.id] = true; // parentId-cycle guard
                            const gkey = 'wg:' + g.id;
                            const open = !this.collapsedTreeNodes[gkey];
                            rows.push({
                                key: gkey, type: 'wgroup', group: g,
                                label: g.name, depth: depth, expanded: open
                            });
                            if (!open) return;
                            (g.workflows || []).forEach(wid => {
                                if (owner[String(wid)] !== g.id) return;
                                const wf = this.workflows.find(w =>
                                    String(w.id) === String(wid));
                                if (wf) emitWorkflow(wf, depth + 1);
                            });
                            emitGroups(g.id, depth + 1);
                        });
                };
                emitGroups(null, 0);
                this.workflows.forEach(wf => {
                    if (!owner[String(wf.id)]) emitWorkflow(wf, 0);
                });
                return rows;
            },

            /**
             * Rows for the "Components" super-group: the SELECTED
             * workflow's role:"component" definition childFlows, flat at
             * depth 0 under the section header. Component definitions
             * live inside the workflow document — there are no global
             * components — so the section is empty until a workflow is
             * selected. Folder membership never hides a component here
             * (kind beats organization).
             */
            componentTreeRows() {
                void this.flowTreeVersion; // reactivity dependency
                if (!this.selectedWorkflow) return [];
                return this.flowTree.components.map(m => ({
                    key: 'flow:' + m.childFlow, type: 'flow',
                    item: m, label: m.label, depth: 0
                }));
            },

            /**
             * All sidebar rows (Workflows section + Components section),
             * kept for tests and external consumers that want the full
             * flattened tree.
             */
            treeRows() {
                return this.workflowTreeRows.concat(this.componentTreeRows);
            },

            /**
             * Role of the child flow on the visible canvas ('flow' |
             * 'component'); used by the palette to offer port markers
             * only inside definition childFlows.
             */
            activeFlowRole() {
                return this.activeTab ? this.flowRoleFor(this.activeTab) : 'flow';
            },

            /**
             * Component definitions derived from the materialized
             * role:"component" canvas childFlows (component_child_flow_tree —
             * definitions became childFlows; the type:"component" node is
             * only the serialization format). Each entry mirrors the
             * old def shape ({id, data:{name,in,out}}) the palette and
             * export paths consume; in/out are length-only placeholders
             * built from the child flow's port markers.
             */
            componentDefs() {
                void this.flowTreeVersion; // reactivity dependency
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const defs = [];
                Object.keys(df).forEach(name => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(name) || {}) : {};
                    if ((meta.role || 'flow') !== 'component') return;
                    const flow = df[name].data || {};
                    let ins = 0;
                    let outs = 0;
                    Object.keys(flow).forEach(id => {
                        if (flow[id].class === 'componentIn') ins++;
                        else if (flow[id].class === 'componentOut') outs++;
                    });
                    defs.push({
                        id: meta.id || name,
                        childFlow: name,
                        data: {
                            name: meta.label || name,
                            in: new Array(ins),
                            out: new Array(outs)
                        }
                    });
                });
                return defs;
            }
        },
        methods: {
            /**
             * Toggle a palette category's collapsed state (header click
             * in the Add Node modal).
             */
            togglePaletteGroup(name) {
                this.paletteCollapsed = Object.assign({}, this.paletteCollapsed, {
                    [name]: !this.paletteCollapsed[name]
                });
            },

            /**
             * POST to the JSON API and unwrap the {status, data, message} envelope
             */
            async api(path, body) {
                const res = await fetch(path, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(body || {})
                });
                const envelope = await res.json();
                if (!res.ok || envelope.status !== 'success') {
                    throw new Error(envelope.message || ('Request failed: ' + res.status));
                }
                return envelope.data || {};
            },

            /**
             * GET variant of api() for the read-only pending-count
             * endpoint — same envelope unwrap, no body.
             */
            async apiGet(path) {
                const res = await fetch(path, { headers: { 'Accept': 'application/json' } });
                const envelope = await res.json();
                if (!res.ok || envelope.status !== 'success') {
                    throw new Error(envelope.message || ('Request failed: ' + res.status));
                }
                return envelope.data || {};
            },

            /**
             * Load the workflow list from the API
             */
            async loadWorkflows() {
                try {
                    const data = await this.api('/api/workflow-fetch-all');
                    this.workflows = data.workflows || [];
                } catch (err) {
                    alert('Failed to load workflows: ' + err.message);
                }
                await this.loadWorkflowGroups();
            },

            /**
             * Fetch the workflow-group list (global-config
             * "workflowGroups" section). Server-persisted so groups
             * follow the user across browsers; a backend without
             * GlobalConfigStore support degrades to a flat list.
             */
            async loadWorkflowGroups() {
                try {
                    const data = await this.api('/api/workflow-groups-fetch');
                    const raw = Array.isArray(data.workflow_groups) ? data.workflow_groups : [];
                    this.workflowGroups = raw
                        .filter(g => g && g.id)
                        .map(g => ({
                            id: String(g.id),
                            name: String(g.name || 'Group'),
                            parentId: g.parentId ? String(g.parentId) : null,
                            workflows: Array.isArray(g.workflows) ? g.workflows.map(String) : []
                        }));
                } catch (err) {
                    this.workflowGroups = [];
                    console.warn('Failed to load workflow groups:', err.message);
                }
            },

            /**
             * Persist the workflow-group list, sanitized: parent ids
             * pointing at deleted groups and workflow refs that no
             * longer resolve are dropped; a workflow id claimed by two
             * groups stays with the first.
             */
            serializeWorkflowGroups() {
                const ids = {};
                this.workflowGroups.forEach(g => { ids[g.id] = true; });
                const wfIds = {};
                this.workflows.forEach(w => { wfIds[String(w.id)] = true; });
                const claimed = {};
                return this.workflowGroups.map(g => ({
                    id: g.id,
                    name: g.name,
                    parentId: (g.parentId && ids[g.parentId]) ? g.parentId : null,
                    workflows: (g.workflows || [])
                        .map(String)
                        .filter(id => wfIds[id])
                        .filter(id => {
                            if (claimed[id]) return false;
                            claimed[id] = true;
                            return true;
                        })
                }));
            },

            /**
             * Persist the groups. Saves are serialized through a
             * promise chain — back-to-back edits (create + move)
             * would otherwise overlap server-side and the last
             * read-modify-write would win.
             */
            saveWorkflowGroups() {
                const body = { workflow_groups: this.serializeWorkflowGroups() };
                this._wgSave = (this._wgSave || Promise.resolve())
                    .then(() => this.api('/api/workflow-groups-update', body))
                    .catch(err => {
                        this.showToast('Workflow groups save failed: ' + err.message, 'danger');
                    });
                return this._wgSave;
            },

            workflowById(id) {
                return this.workflows.find(w => String(w.id) === String(id)) || null;
            },

            workflowGroupById(id) {
                return this.workflowGroups.find(g => g.id === id) || null;
            },

            /** Display path "a/b/c" for a workflow group (menu label). */
            workflowGroupPathFor(group) {
                const parts = [group.name];
                let cur = group;
                let guard = 0;
                while (cur.parentId && guard++ < 32) {
                    cur = this.workflowGroupById(cur.parentId);
                    if (!cur) break;
                    parts.unshift(cur.name);
                }
                return parts.join(' / ');
            },

            /** The group claiming a workflow (first wins), else null. */
            workflowGroupFor(workflowId) {
                const wid = String(workflowId);
                return this.workflowGroups.find(g =>
                    (g.workflows || []).some(id => String(id) === wid)) || null;
            },

            /** Create a workflow group under parentId (null = root). */
            createWorkflowGroup(parentId) {
                const name = (window.prompt('Workflow group name', 'New group') || '').trim();
                if (!name) return;
                const g = {
                    id: uuidV7(),
                    name: name,
                    parentId: parentId || null,
                    workflows: []
                };
                this.workflowGroups.push(g);
                this.collapsedTreeNodes['wg:' + g.id] = false;
                return this.saveWorkflowGroups();
            },

            renameWorkflowGroup(group) {
                if (!group) return;
                const name = (window.prompt('Rename workflow group', group.name) || '').trim();
                if (!name || name === group.name) return;
                group.name = name;
                return this.saveWorkflowGroups();
            },

            /**
             * Delete a workflow group and its descendant groups.
             * Organization only — member workflows are never touched
             * and return to the ungrouped root list.
             */
            deleteWorkflowGroup(group) {
                if (!group) return;
                if (!confirm(`Delete workflow group "${group.name}"? Workflows inside move back to the root — nothing else is removed.`)) return;
                const doomed = {};
                const mark = id => {
                    doomed[id] = true;
                    this.workflowGroups.filter(g => g.parentId === id).forEach(g => mark(g.id));
                };
                mark(group.id);
                this.workflowGroups = this.workflowGroups.filter(g => !doomed[g.id]);
                return this.saveWorkflowGroups();
            },

            /**
             * Move a workflow into a group (null = ungrouped root).
             * Membership is exclusive: the workflow id leaves every
             * other group's list.
             */
            moveWorkflowToGroup(workflowId, groupId) {
                const wid = String(workflowId);
                this.workflowGroups.forEach(g => {
                    g.workflows = (g.workflows || []).filter(id => String(id) !== wid);
                });
                const group = groupId ? this.workflowGroupById(groupId) : null;
                if (group) {
                    if (!Array.isArray(group.workflows)) group.workflows = [];
                    group.workflows.push(wid);
                    this.collapsedTreeNodes['wg:' + group.id] = false;
                }
                return this.saveWorkflowGroups();
            },

            /**
             * Per-type overrides for inputs/outputs/color that the backend
             * does not provide. Anything not listed falls back to defaults
             * derived from the node category (see normalizeNodeType).
             */
            nodeTypeOverrides() {
                return {
                    // API-provided types
                    httpTrigger: { inputs: 0, outputs: 1, color: '#4CAF50' },
                    submitIn:    { inputs: 0, outputs: 1, color: '#4CAF50' },
                    inject:      { inputs: 0, outputs: 1, color: '#4CAF50' },
                    httpRequest: { inputs: 1, outputs: 1, color: '#2196F3' },
                    httpResponse:{ inputs: 1, outputs: 0, color: '#F44336' },
                    log:         { inputs: 1, outputs: 1, color: '#9C27B0' },
                    debug:       { inputs: 1, outputs: 0, color: '#9C27B0' },
                    delay:       { inputs: 1, outputs: 1, color: '#FF9800' },
                    condition:   { inputs: 1, outputs: 2, color: '#FFC107' },
                    switch:      { inputs: 1, color: '#FFC107' },
                    change:      { inputs: 1, outputs: 1, color: '#9C27B0' },
                    split:       { inputs: 1, outputs: 1, color: '#009688' },
                    join:        { inputs: 1, outputs: 1, color: '#009688' },
                    sort:        { inputs: 1, outputs: 1, color: '#009688' },
                    batch:       { inputs: 1, outputs: 1, color: '#009688' },
                    range:       { inputs: 1, outputs: 1, color: '#009688' },
                    rbe:         { inputs: 1, outputs: 1, color: '#009688' },
                    function:    { inputs: 1, outputs: 1, color: '#FDD835' },
                    trigger:     { inputs: 1, outputs: 2, color: '#FF9800' },
                    wait:        { inputs: 1, outputs: 2, color: '#FF9800' },
                    notify:      { inputs: 1, outputs: 1, color: '#FF9800' },
                    template:    { inputs: 1, outputs: 1, color: '#9C27B0' },
                    json:        { inputs: 1, outputs: 2, color: '#607D8B' },
                    yaml:        { inputs: 1, outputs: 2, color: '#607D8B' },
                    xml:         { inputs: 1, outputs: 1, color: '#607D8B' },
                    csv:         { inputs: 1, outputs: 1, color: '#607D8B' },
                    junction:    { inputs: 1, outputs: 1, color: '#9E9E9E' },
                    linkIn:      { inputs: 0, outputs: 1, color: '#2196F3' },
                    linkOut:     { inputs: 1, outputs: 1, color: '#2196F3' },
                    linkCall:    { inputs: 1, outputs: 1, color: '#2196F3' },
                    catch:       { inputs: 0, outputs: 1, color: '#795548' },
                    status:      { inputs: 0, outputs: 1, color: '#795548' },
                    complete:    { inputs: 0, outputs: 1, color: '#795548' },
                    exec:        { inputs: 1, outputs: 3, color: '#455A64' },
                    tcpIn:       { inputs: 0, outputs: 1, color: '#455A64' },
                    tcpOut:      { inputs: 1, outputs: 0, color: '#455A64' },
                    mqttIn:      { inputs: 0, outputs: 1, color: '#455A64' },
                    mqttOut:     { inputs: 1, outputs: 0, color: '#455A64' },
                    mailIn:      { inputs: 0, outputs: 1, color: '#455A64' },
                    mailOut:     { inputs: 1, outputs: 0, color: '#455A64' },
                    websocketIn: { inputs: 0, outputs: 1, color: '#455A64' },
                    websocketOut:{ inputs: 1, outputs: 0, color: '#455A64' },
                    udpIn:       { inputs: 0, outputs: 1, color: '#455A64' },
                    udpOut:      { inputs: 1, outputs: 0, color: '#455A64' },
                    file:        { inputs: 1, outputs: 1, color: '#607D8B' },
                    db:          { inputs: 1, outputs: 1, color: '#795548' },
                    fileIn:      { inputs: 1, outputs: 1, color: '#607D8B' },
                    fsIn:        { inputs: 0, outputs: 1, color: '#607D8B' },
                    fsOut:       { inputs: 1, outputs: 1, color: '#607D8B' },
                    queue:       { inputs: 1, outputs: 1, color: '#3F51B5' },
                    checkpoint:  { inputs: 1, color: '#3F51B5' },
                    comment:     { inputs: 0, outputs: 0, color: '#90A4AE' },
                    unknown:     { inputs: 1, outputs: 1, color: '#6c757d' },
                    tcpRequest:  { inputs: 1, outputs: 1, color: '#455A64' },
                    tlsConfig:   { inputs: 0, outputs: 0, color: '#455A64' },
                    html:        { inputs: 1, outputs: 1, color: '#607D8B' },
                    watch:       { inputs: 0, outputs: 1, color: '#607D8B' },
                    tail:        { inputs: 0, outputs: 1, color: '#607D8B' }
                };
            },

            /**
             * Category-based fallbacks for types without an explicit override
             */
            nodeCategoryDefaults() {
                return {
                    'Triggers':     { inputs: 0, outputs: 1, color: '#4CAF50' },
                    'Actions':      { inputs: 1, outputs: 1, color: '#2196F3' },
                    'Debug':        { inputs: 1, outputs: 1, color: '#9C27B0' },
                    'Flow Control': { inputs: 1, outputs: 1, color: '#FF9800' },
                    'Parsers':      { inputs: 1, outputs: 1, color: '#607D8B' },
                    'Network':      { inputs: 1, outputs: 1, color: '#455A64' },
                    'Storage':      { inputs: 1, outputs: 1, color: '#607D8B' },
                    'Queue':        { inputs: 1, outputs: 1, color: '#3F51B5' },
                    'Common':       { inputs: 1, outputs: 1, color: '#90A4AE' }
                };
            },

            /**
             * Map a backend node type ({type, name, description, icon, category})
             * to the editor shape ({id, name, description, icon, category,
             * inputs, outputs, color}).
             */
            normalizeNodeType(t) {
                const overrides = this.nodeTypeOverrides()[t.type] || {};
                const categoryDefaults = this.nodeCategoryDefaults()[t.category] || {};
                return {
                    id: t.type,
                    name: t.name || t.type,
                    description: t.description || '',
                    icon: t.icon || '',
                    category: t.category || '',
                    inputs: overrides.inputs !== undefined ? overrides.inputs
                        : (categoryDefaults.inputs !== undefined ? categoryDefaults.inputs : 1),
                    outputs: overrides.outputs !== undefined ? overrides.outputs
                        : (categoryDefaults.outputs !== undefined ? categoryDefaults.outputs : 1),
                    color: overrides.color || categoryDefaults.color || '#607D8B'
                };
            },

            /**
             * Resolve a type id against core node types first, then the
             * component palette entries (for "component:<defId>" ids).
             */
            findType(typeId) {
                return this.nodeTypes.find(t => t.id === typeId) ||
                    this.pendingComponentTypes[typeId] ||
                    this.componentTypes.find(t => t.id === typeId) ||
                    COMPONENT_PORT_TYPES.find(t => t.id === typeId);
            },

            /**
             * Load node types from the API; merges with (and extends) the
             * built-in fallback list so new backend types appear automatically.
             */
            async loadNodeTypes() {
                try {
                    const data = await this.api('/api/node-fetch-all');
                    const remote = (data.node_types || []).map(t => this.normalizeNodeType(t));
                    const known = {};
                    this.nodeTypes.forEach(t => { known[t.id] = t; });
                    remote.forEach(t => {
                        // Merge: keep local entries too, but let the API
                        // provide the authoritative name/description/icon.
                        known[t.id] = Object.assign({}, known[t.id] || {}, t);
                    });
                    this.nodeTypes = Object.values(known);
                } catch (err) {
                    // Keep the hardcoded fallback list
                    console.warn('Failed to load node types from API:', err.message);
                }
            },

            /**
             * Fetch a workflow and load its nodes/edges onto the canvas
             */
            async selectWorkflow(workflow) {
                try {
                    const data = await this.api('/api/workflow-fetch', { id: workflow.id });
                    this.selectedWorkflow = data.workflow;
                    const viewEnv = data.workflow && data.workflow.view && data.workflow.view.env;
                    this.flowEnv = Array.isArray(viewEnv) ? viewEnv : [];
                    this.clearNodeStatuses();
                    this.renderFlow(this.selectedWorkflow);
                    this.applyView(this.selectedWorkflow.view);
                    this.dirty = false;
                    this.syncWorkflowParam();
                } catch (err) {
                    this.showToast('Failed to load workflow: ' + err.message, 'error');
                }
            },

            /**
             * Deep links: keep ?workflow=<id> in the URL in sync with the
             * selection so an open flow is shareable. pushState (not
             * replaceState) so the back button walks workflow history —
             * onPopState below navigates. Other params (env=global and
             * friends) are preserved; a no-op when the URL already
             * matches, which also stops popstate-driven selects from
             * re-pushing the same entry.
             */
            syncWorkflowParam() {
                const url = new URL(window.location.href);
                if (this.selectedWorkflow) {
                    url.searchParams.set('workflow', String(this.selectedWorkflow.id));
                } else {
                    url.searchParams.delete('workflow');
                }
                if (url.href !== window.location.href) {
                    window.history.pushState({}, '', url.href);
                }
            },

            /**
             * Back/forward navigation: select the workflow named in the
             * URL, or clear the workspace when the param is gone.
             */
            onPopState() {
                const id = new URLSearchParams(window.location.search).get('workflow');
                if (id && (!this.selectedWorkflow || String(this.selectedWorkflow.id) !== id)) {
                    this.selectWorkflow({ id });
                } else if (!id && this.selectedWorkflow) {
                    this.deselectWorkflow();
                }
            },

            /**
             * Apply ?workflow=<id> once the list has loaded (deep-link
             * entry). Unknown ids fail soft — the fetch error becomes a
             * toast and the editor stays on the welcome screen.
             */
            applyWorkflowParam() {
                const id = new URLSearchParams(window.location.search).get('workflow');
                if (id) this.selectWorkflow({ id });
            },

            /**
             * Clear the selection entirely: tabs close, the workspace is
             * hidden and the welcome screen takes over.
             */
            deselectWorkflow() {
                this.selectedWorkflow = null;
                this.flowTabs = [];
                this.activeTab = null;
                this.dirty = false;
            },

            /**
             * Rebuild the canvas from a backend {nodes, edges} workflow
             */
            /**
             * Id for the next Drawflow node: the queued backend id while a
             * workflow is rebuilt, otherwise a fresh UUIDv7.
             */
            nextNodeId() {
                return this.pendingNodeIds.length ? this.pendingNodeIds.shift() : uuidV7();
            },

            renderFlow(workflow) {
                this.suppressHistory = true;
                try {
                    this.resetChildFlows();
                    this.drawflowToBackend = {};
                    this.backendToDrawflow = {};

                    // Component definitions live in the nodes list as
                    // type:"component" but are templates: each becomes a
                    // role:"component" canvas child flow (component_child_flow_tree)
                    // rendered by renderComponentDef below.
                    const componentDefNodes = (workflow.nodes || []).filter(n => n.type === 'component');
                    const componentDefIds = {};
                    componentDefNodes.forEach(d => { componentDefIds[String(d.id)] = true; });

                    // Materialize persisted child flow entities first
                    // (view.childFlows): each becomes a canvas child flow
                    // keyed by its label with its stable meta.id
                    // restored, so node data.childFlow=id references keep
                    // pointing at the same child flow across save/load.
                    // A role:"component" entity whose id matches a
                    // definition node is skipped — the definition
                    // materializes its own child flow below; an orphan
                    // entity (imported shell with no def) still gets a
                    // child flow so nothing silently vanishes.
                    const savedFlows = (workflow.view && Array.isArray(workflow.view.childFlows))
                        ? workflow.view.childFlows : [];
                    savedFlows.forEach(m => {
                        if (!m || !m.id) return;
                        if (m.role === 'component' && componentDefIds[String(m.id)]) return;
                        const key = m.label || 'Home';
                        if (!this.editor.drawflow.drawflow[key]) this.editor.addModule(key);
                        if (typeof this.editor.setModuleMeta === 'function') {
                            this.editor.setModuleMeta(key, {
                                id: m.id,
                                label: key,
                                role: m.role,
                                enabled: m.enabled,
                                env: m.env
                            });
                        }
                    });

                    // Materialize each component definition as a
                    // role:"component" canvas child flow holding its inner
                    // graph plus in/out port markers. Two passes: every
                    // child flow entity must exist (with its port-counting
                    // meta) before inner graphs render, so a nested
                    // "component:<id>" instance resolves its def's ports
                    // regardless of definition order.
                    this.pendingComponentTypes = {};
                    componentDefNodes.forEach(d => {
                        const data = d.data || {};
                        this.pendingComponentTypes['component:' + d.id] = {
                            id: 'component:' + d.id,
                            name: 'component: ' + (data.name || d.id),
                            inputs: Array.isArray(data.in) ? data.in.length : 0,
                            outputs: Array.isArray(data.out) ? data.out.length : 0,
                            color: '#7e8ba0',
                            icon: 'object-group',
                            category: 'Components'
                        };
                    });
                    const componentDefKeys = componentDefNodes.map(d => this.prepareComponentDefFlow(d));
                    componentDefNodes.forEach((d, i) => this.populateComponentDefFlow(d, componentDefKeys[i]));

                    // Group nodes by their tab. data.childFlow stores the
                    // child flow id; legacy workflows store the child flow
                    // name — resolve either to the canvas child flow key.
                    const groups = {}; // canvas child flow name -> [backend nodes]
                    (workflow.nodes || []).forEach(node => {
                        if (node.type === 'component') return;
                        const flow = this.resolveFlowName(
                            (node.data && node.data.childFlow) || 'Home');
                        (groups[flow] = groups[flow] || []).push(node);
                    });
                    const childFlowKeys = Object.keys(groups);
                    if (!childFlowKeys.length) childFlowKeys.push('Home');
                    childFlowKeys.forEach(m => {
                        if (!this.editor.drawflow.drawflow[m]) this.editor.addModule(m);
                    });
                    // Tabs cover every materialized canvas child flow —
                    // populated ones first, then node-less childFlows
                    // restored from view.childFlows.
                    const savedLabels = savedFlows
                        .filter(m => m && m.id)
                        .map(m => m.label || 'Home');
                    const ordered = childFlowKeys
                        .concat(savedLabels.filter(n => childFlowKeys.indexOf(n) === -1))
                        .concat(componentDefKeys.filter(n => childFlowKeys.indexOf(n) === -1 && savedLabels.indexOf(n) === -1));
                    // Drop canvas childFlows not backed by nodes or a
                    // saved entity — the leftover kept by resetChildFlows
                    // (e.g. 'Home' after the workflow's Home child flow was
                    // renamed) must not resurface as a phantom tab.
                    const wanted = {};
                    ordered.forEach(n => { wanted[n] = true; });
                    Object.keys(this.editor.drawflow.drawflow || {}).forEach(n => {
                        if (!wanted[n]) {
                            try { this.editor.removeModule(n); } catch (err) { /* ignore */ }
                        }
                    });
                    // Tabs are only the OPEN childFlows (child flow navigator):
                    // every child flow stays materialized in Drawflow and in
                    // the tree, but the tab bar shows the persisted open
                    // set — view.openFlows (child flow ids). Documents
                    // without the key predate the navigator and open every
                    // child flow, matching the old 1:1 tab behavior.
                    const openIds = (workflow.view && Array.isArray(workflow.view.openFlows))
                        ? workflow.view.openFlows.map(v => this.resolveFlowName(String(v)))
                        : null;
                    const openNames = openIds
                        ? ordered.filter(m => openIds.indexOf(m) !== -1)
                        // Documents predating the navigator open every
                        // flow child flow; definition childFlows stay closed
                        // (opened from the tree) so a load does not
                        // spawn a tab per component.
                        : ordered.filter(m => this.flowRoleFor(m) !== 'component');
                    if (!openNames.length && ordered.length) openNames.push(ordered[0]);
                    this.flowTabs = openNames.map(m => ({
                        childFlow: m,
                        label: this.flowLabelFor(m),
                        role: this.flowRoleFor(m)
                    }));
                    this.knownFlows = ordered.slice();
                    this.flowTreeVersion++;

                    childFlowKeys.forEach(flow => {
                        this.editor.changeModule(flow);
                        const idMap = {}; // backend node id -> Drawflow node id (within this child flow)
                        (groups[flow] || []).forEach(node => {
                            const type = this.findType(node.type);
                            this.pendingNodeIds = [String(node.id)];
                            const drawflowId = this.addNode(
                                node.type,
                                (node.data && node.data.name) || (type ? type.name : node.type),
                                node.position ? node.position.x : 100,
                                node.position ? node.position.y : 100,
                                node.data || {}
                            );
                            this.pendingNodeIds = [];
                            idMap[node.id] = drawflowId;
                            this.drawflowToBackend[drawflowId] = String(node.id);
                            this.backendToDrawflow[String(node.id)] = String(drawflowId);
                            this.setNodeDisabledDom(drawflowId, this.isNodeDisabledData(node.data));
                        });
                        // Edges only apply when both endpoints live in this child flow
                        (workflow.edges || []).forEach(edge => {
                            if (idMap[edge.source] === undefined || idMap[edge.target] === undefined) return;
                            this.editor.addConnection(
                                idMap[edge.source],
                                idMap[edge.target],
                                edge.sourceKey || 'output_1',
                                edge.targetKey || 'input_1'
                            );
                        });
                    });
                    this.editor.changeModule(this.flowTabs[0].childFlow);
                    this.activeTab = this.flowTabs[0].childFlow;
                } finally {
                    this.suppressHistory = false;
                    this.pendingComponentTypes = {};
                }
                // Per-workflow history: clear stacks on workflow switch (task 09)
                this.clearHistory();
                this.refreshAllOutputLabels();
                this.scheduleMinimap();
            },

            /**
             * Remove all nodes from every child flow and delete extra childFlows,
             * leaving a single empty child flow named 'Home'
             */
            resetChildFlows() {
                const childFlows = Object.keys(this.editor.drawflow.drawflow || {});
                childFlows.forEach(m => {
                    this.editor.changeModule(m);
                    this.editor.clearModuleSelected();
                });
                const keep = childFlows.indexOf('Home') !== -1 ? 'Home' : childFlows[0];
                if (keep) {
                    this.editor.changeModule(keep);
                    childFlows.forEach(m => {
                        if (m !== keep) {
                            try { this.editor.removeModule(m); } catch (err) { /* ignore */ }
                        }
                    });
                } else {
                    this.editor.addModule('Home');
                    this.editor.changeModule('Home');
                }
            },

            /**
             * Pass 1 of definition materialization (component_child_flow_tree):
             * create the role:"component" canvas child flow and its meta for
             * one type:"component" node. The child flow key is the def name
             * deduped against existing canvas childFlows; the def id
             * becomes meta.id so "component:<id>" instance types keep
             * resolving. Returns the canvas child flow key.
             */
            prepareComponentDefFlow(def) {
                const data = def.data || {};
                const df = this.editor.drawflow.drawflow;
                const base = String(data.name || '').trim() || 'Component';
                let key = base;
                for (let i = 2; df[key]; i++) key = base + ' ' + i;
                this.editor.addModule(key);
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(key, {
                        id: String(def.id),
                        label: base,
                        role: 'component',
                        enabled: true,
                        env: Array.isArray(data.env) ? data.env : []
                    });
                }
                return key;
            },

            /**
             * Pass 2 of definition materialization: fill the child flow
             * created by prepareComponentDefFlow with the inner
             * nodes/edges plus one componentIn/componentOut marker node per
             * in[]/out[] port.
             */
            populateComponentDefFlow(def, key) {
                const data = def.data || {};
                this.editor.changeModule(key);

                const idMap = {}; // persisted id -> drawflow id (within this child flow)
                const portX = (p, fallback) => (p && typeof p.x === 'number') ? p.x : fallback;
                const portY = (p, fallback) => (p && typeof p.y === 'number') ? p.y : fallback;

                // Inner nodes accept both the backend shape
                // ({id,type,position:{x,y},data:{...}}) and the Node-RED
                // flat shape ({id,type,x,y,...config}) — the same
                // contract as the backend's parseComponentInnerNode.
                const inner = Array.isArray(data.nodes) ? data.nodes : [];
                inner.forEach(n => {
                    if (!n || n.id === undefined || n.id === null) return;
                    const pos = n.position || { x: n.x || 100, y: n.y || 100 };
                    let ndata;
                    if (n.data && typeof n.data === 'object') {
                        ndata = Object.assign({}, n.data);
                    } else {
                        ndata = {};
                        Object.keys(n).forEach(k => {
                            if (['id', 'type', 'x', 'y', 'z', 'wires', 'position', 'data'].indexOf(k) === -1) {
                                ndata[k] = n[k];
                            }
                        });
                    }
                    const type = this.findType(n.type);
                    this.pendingNodeIds = [String(n.id)];
                    const drawflowId = this.addNode(
                        n.type,
                        ndata.name || (type ? type.name : n.type),
                        pos.x !== undefined ? pos.x : 100,
                        pos.y !== undefined ? pos.y : 100,
                        ndata);
                    this.pendingNodeIds = [];
                    idMap[String(n.id)] = drawflowId;
                });

                // Port markers — one canvas node per in[]/out[] entry,
                // portIndex pins the port's position in the array.
                const ins = Array.isArray(data.in) ? data.in : [];
                const outs = Array.isArray(data.out) ? data.out : [];
                ins.forEach((p, i) => {
                    idMap['__in_' + i] = this.addNode(
                        'componentIn', 'input ' + (i + 1),
                        portX(p, 40), portY(p, 40 + i * 60),
                        { name: 'input ' + (i + 1), portIndex: i });
                });
                outs.forEach((p, i) => {
                    idMap['__out_' + i] = this.addNode(
                        'componentOut', 'output ' + (i + 1),
                        portX(p, 400), portY(p, 40 + i * 60),
                        { name: 'output ' + (i + 1), portIndex: i });
                });

                const connect = (srcId, dstId, srcKey, dstKey) => {
                    if (srcId === undefined || dstId === undefined) return;
                    try {
                        this.editor.addConnection(srcId, dstId, srcKey, dstKey);
                    } catch (err) { /* ignore wires dangling past missing nodes */ }
                };
                // Inner edges use the backend shape
                // {source, sourceKey, target, targetKey}.
                (Array.isArray(data.edges) ? data.edges : []).forEach(e => {
                    if (!e) return;
                    connect(idMap[String(e.source)], idMap[String(e.target)],
                        e.sourceKey || 'output_1', e.targetKey || 'input_1');
                });
                // Port wires: in[i].wires entries feed the listed
                // targets; out[i].wires entries {id, port} feed the
                // marker from the inner node's output_{port+1}.
                ins.forEach((p, i) => {
                    ((p && p.wires) || []).forEach(w => {
                        connect(idMap['__in_' + i], idMap[String(w.id)], 'output_1', 'input_1');
                    });
                });
                outs.forEach((p, i) => {
                    ((p && p.wires) || []).forEach(w => {
                        connect(idMap[String(w.id)], idMap['__out_' + i],
                            'output_' + ((w.port || 0) + 1), 'input_1');
                    });
                });
                return key;
            },

            /* ---- Child flow identity (id-keyed data.childFlow migration) ---- */

            /**
             * Resolve a stored data.childFlow reference — a child flow id
             * (current model) or a legacy child flow name — to the Drawflow
             * canvas child flow key. Canvas keys stay names; unresolvable
             * refs return themselves so the caller can still
             * materialize a child flow for them.
             */
            resolveFlowName(ref) {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                if (df[ref]) return ref; // already a canvas child flow key
                if (this.editor && typeof this.editor.getModuleById === 'function') {
                    const name = this.editor.getModuleById(ref);
                    if (name) {
                        console.debug('Resolved child flow id to canvas childFlow:', ref, '->', name);
                        return name;
                    }
                }
                return ref;
            },

            /**
             * Stable child flow id for a canvas child flow name (meta.id),
             * falling back to the name itself when the fork lacks meta.
             */
            flowIdFor(name) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? this.editor.getModuleMeta(name) : null;
                return (meta && meta.id) || name;
            },

            /**
             * Display label for a canvas childFlow: meta.label when the
             * fork carries it, with the legacy Home -> Main cosmetic
             * rename preserved.
             */
            flowLabelFor(name) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? this.editor.getModuleMeta(name) : null;
                const label = (meta && meta.label) || name;
                return name === 'Home' ? 'Main' : label;
            },

            /**
             * Child flow role ('flow' | 'component') from meta; defaults to
             * 'flow' so every child flow sorts into a tree group.
             */
            flowRoleFor(name) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? this.editor.getModuleMeta(name) : null;
                return (meta && meta.role) || 'flow';
            },

            /**
             * Child flow entities persisted as view.childFlows: the
             * {id, label, role, enabled, env} meta of every canvas
             * child flow, so ids survive save/load and child flow state
             * (role/enabled/env) outlives node membership.
             */
            serializeChildFlows() {
                if (!this.editor || typeof this.editor.getModuleMeta !== 'function') return [];
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                return Object.keys(df)
                    .map(name => this.editor.getModuleMeta(name))
                    .filter(Boolean);
            },

            /**
             * Convert the Drawflow export into backend {nodes, edges}
             */
            exportFlow() {
                const exported = this.editor.export();
                const childFlows = (exported && exported.drawflow) || {};
                const nodes = [];
                const edges = [];
                // Merge every Drawflow child flow (tab) into the single backend
                // nodes+edges set. data.childFlow carries the stable child flow
                // id (meta.id); data.childFlowName is a denormalized display
                // label so stored documents stay human-readable. Canvas
                // child flow keys remain names — Drawflow keys child flows by name.
                Object.keys(childFlows).forEach(flowName => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(flowName) || {}) : {};
                    // Component template childFlows serialize as type:"component"
                    // definition nodes (serializeComponentDefs), never as
                    // runnable nodes.
                    if ((meta.role || 'flow') === 'component') return;
                    const flow = childFlows[flowName].data || {};
                    const markerIds = {};
                    Object.keys(flow).forEach(id => {
                        if (isComponentPortClass(flow[id].class)) markerIds[id] = true;
                    });
                    Object.keys(flow).forEach(id => {
                        const n = flow[id];
                        // Defensive: port markers only belong inside
                        // component childFlows; one stranded in a flow
                        // child flow must not reach the backend as an
                        // unknown type.
                        if (markerIds[id]) return;
                        const data = Object.assign({}, n.data || {});
                        data.childFlow = meta.id || flowName;
                        if (meta.label) data.childFlowName = meta.label;
                        nodes.push({
                            id: String(id),
                            type: n.class || 'unknown',
                            position: { x: n.pos_x, y: n.pos_y },
                            data: data
                        });
                        // Derive edges from each node's output connections
                        Object.keys(n.outputs || {}).forEach(outputKey => {
                            (n.outputs[outputKey].connections || []).forEach(conn => {
                                if (markerIds[conn.node]) return;
                                edges.push({
                                    id: 'edge-' + flowName + '-' + id + '-' + outputKey + '-' + conn.node + '-' + conn.output,
                                    source: String(id),
                                    sourceKey: outputKey,
                                    target: String(conn.node),
                                    targetKey: conn.output // target input port name (e.g. input_1)
                                });
                            });
                        });
                    });
                });
                return { nodes, edges };
            },

            /**
             * Serialize every role:"component" canvas child flow back into a
             * type:"component" definition node (component_child_flow_tree) —
             * the inverse of renderComponentDef. Port markers become
             * in[]/out[] entries ordered by their data.portIndex; the
             * remaining nodes/edges become the definition's internal
             * graph. The def id is the child flow's stable meta.id so
             * "component:<id>" instances keep resolving across save/load.
             */
            serializeComponentDefs() {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const defs = [];
                Object.keys(df).forEach(name => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(name) || {}) : {};
                    if ((meta.role || 'flow') !== 'component') return;
                    const flow = df[name].data || {};
                    const markerIds = {}; // drawflow id -> 'in' | 'out'
                    const markers = { in: [], out: [] };
                    Object.keys(flow).forEach(id => {
                        const cls = flow[id].class;
                        if (cls === 'componentIn') {
                            markerIds[id] = 'in';
                            markers.in.push({ id: id, node: flow[id] });
                        } else if (cls === 'componentOut') {
                            markerIds[id] = 'out';
                            markers.out.push({ id: id, node: flow[id] });
                        }
                    });
                    const byPortIndex = (a, b) => {
                        const pa = (a.node.data && typeof a.node.data.portIndex === 'number')
                            ? a.node.data.portIndex : 1e9;
                        const pb = (b.node.data && typeof b.node.data.portIndex === 'number')
                            ? b.node.data.portIndex : 1e9;
                        if (pa !== pb) return pa - pb;
                        return (a.node.pos_y - b.node.pos_y) || (a.node.pos_x - b.node.pos_x);
                    };
                    markers.in.sort(byPortIndex);
                    markers.out.sort(byPortIndex);

                    // Internal graph = everything but the markers. A
                    // connection into an out-marker records an
                    // out[].wires entry {id, port}; a connection out of
                    // an in-marker records an in[].wires entry {id}.
                    const nodes = [];
                    const edges = [];
                    const outWires = {}; // marker drawflow id -> [{id, port}]
                    Object.keys(flow).forEach(id => {
                        const n = flow[id];
                        Object.keys(n.outputs || {}).forEach(outputKey => {
                            const port = parseInt(String(outputKey).replace('output_', ''), 10) - 1;
                            (n.outputs[outputKey].connections || []).forEach(conn => {
                                const kind = markerIds[conn.node];
                                if (kind === 'out') {
                                    (outWires[conn.node] = outWires[conn.node] || []).push({
                                        id: String(id),
                                        port: isNaN(port) ? 0 : port
                                    });
                                } else if (!kind && !markerIds[id]) {
                                    edges.push({
                                        id: 'edge-' + name + '-' + id + '-' + outputKey + '-' + conn.node + '-' + conn.output,
                                        source: String(id),
                                        sourceKey: outputKey,
                                        target: String(conn.node),
                                        targetKey: conn.output
                                    });
                                }
                            });
                        });
                        if (markerIds[id]) return;
                        const data = Object.assign({}, n.data || {});
                        delete data.childFlow;
                        delete data.childFlowName;
                        delete data.portIndex;
                        nodes.push({
                            id: String(id),
                            type: n.class || 'unknown',
                            position: { x: n.pos_x, y: n.pos_y },
                            data: data
                        });
                    });

                    const inPorts = markers.in.map(m => ({
                        x: m.node.pos_x,
                        y: m.node.pos_y,
                        wires: (((m.node.outputs || {}).output_1 || {}).connections || [])
                            .filter(c => !markerIds[c.node])
                            .map(c => ({ id: String(c.node) }))
                    }));
                    const outPorts = markers.out.map(m => ({
                        x: m.node.pos_x,
                        y: m.node.pos_y,
                        wires: outWires[m.id] || []
                    }));

                    defs.push({
                        id: String(meta.id || name),
                        type: 'component',
                        position: { x: 0, y: 0 },
                        data: {
                            name: meta.label || name,
                            in: inPorts,
                            out: outPorts,
                            env: Array.isArray(meta.env) ? meta.env : [],
                            nodes: nodes,
                            edges: edges
                        }
                    });
                });
                return defs;
            },

            /**
             * Add a node of the given type to the canvas; returns Drawflow node id
             */
            /**
             * Escape a string for safe interpolation into node HTML
             */
            escapeHtml(s) {
                return String(s == null ? '' : s)
                    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
                    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
            },

            /**
             * Minimal safe markdown renderer for the node info panel:
             * input is HTML-escaped first, then a small inline/block
             * subset (headings, bold, italic, code, links, lists) is
             * applied. Node-RED renders node.info as markdown.
             */
            renderMarkdown(text) {
                if (!text) return '';
                const esc = this.escapeHtml(text);
                const inline = s => s
                    .replace(/`([^`]+)`/g, '<code>$1</code>')
                    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
                    .replace(/\*([^*]+)\*/g, '<em>$1</em>')
                    .replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g,
                        '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
                const out = [];
                let list = null;
                esc.split(/\r?\n/).forEach(line => {
                    const t = line.trim();
                    const heading = t.match(/^(#{1,4})\s+(.*)$/);
                    const item = t.match(/^[-*]\s+(.*)$/);
                    if (item) {
                        if (!list) { list = []; out.push(list); }
                        list.push(item[1]);
                        return;
                    }
                    list = null;
                    if (!t) { out.push(''); return; }
                    if (heading) {
                        const lvl = Math.min(6, heading[1].length + 2);
                        out.push('<h' + lvl + '>' + inline(heading[2]) + '</h' + lvl + '>');
                    } else {
                        out.push('<p class="mb-1">' + inline(t) + '</p>');
                    }
                });
                return out.map(part => {
                    if (Array.isArray(part)) {
                        return '<ul class="mb-1 ps-3">' +
                            part.map(i => '<li>' + inline(i) + '</li>').join('') + '</ul>';
                    }
                    return part;
                }).join('');
            },

            /**
             * Deep-copy a Drawflow export with credential fields removed:
             * credential-typed keys per node type plus any sentinel marker
             * left by the server. Mirrors Node-RED exports, which strip
             * creds-typed properties entirely.
             */
            scrubCredentials(exported) {
                const copy = JSON.parse(JSON.stringify(exported || {}));
                const childFlows = (copy && copy.drawflow) || {};
                Object.keys(childFlows).forEach(flow => {
                    const data = (childFlows[flow] && childFlows[flow].data) || {};
                    Object.keys(data).forEach(id => {
                        const n = data[id];
                        const d = (n && n.data) || {};
                        const type = (d && d.type) || (n && n.class);
                        (CREDENTIAL_KEYS[type] || []).forEach(k => delete d[k]);
                        Object.keys(d).forEach(k => {
                            if (d[k] === CREDENTIAL_SENTINEL) delete d[k];
                        });
                    });
                });
                return copy;
            },

            /**
             * Build a short one-line summary of key config values for display
             * in the node body (like Node-RED's label text). Skips name/type.
             */
            nodeSummary(data) {
                const skip = { name: 1, type: 1, outputs: 1 };
                const parts = [];
                Object.keys(data || {}).forEach(key => {
                    if (skip[key] || parts.length >= 2) return;
                    let v = data[key];
                    if (v === undefined || v === null || v === '') return;
                    if (typeof v === 'object') {
                        const keys = Object.keys(v);
                        if (keys.length === 0) return;
                        v = '{' + keys.length + ' items}';
                    }
                    parts.push(this.escapeHtml(key) + ': ' + this.escapeHtml(v));
                });
                return parts.join(' · ');
            },

            /**
             * Number of output ports a switch node needs:
             * one per rule, plus a trailing "otherwise" port when enabled
             * (matches the engine's SelectPorts contract).
             */
            switchOutputCount(data) {
                const rules = Array.isArray(data && data.rules) ? data.rules.length : 2;
                return Math.max(1, rules + ((data && data.otherwise) ? 1 : 0));
            },

            /**
             * Per-output labels for a switch node (rule 1..n, otherwise)
             */
            switchOutputLabels(data) {
                const rules = Array.isArray(data && data.rules) ? data.rules.length : 2;
                const labels = [];
                for (let i = 1; i <= rules; i++) labels.push('rule ' + i);
                if (data && data.otherwise) labels.push('otherwise');
                return labels;
            },

            /**
             * Ordered route labels for a checkpoint node. routes may be
             * an array of labels or a comma-separated string; blank
             * entries fall back to their "output_N" port key (mirrors
             * the engine's RouteLabels fallback).
             */
            checkpointRouteLabels(data) {
                let routes = data && data.routes;
                if (typeof routes === 'string') {
                    routes = routes.split(',').map(s => s.trim());
                }
                if (!Array.isArray(routes) || routes.length === 0) {
                    routes = ['approve', 'decline'];
                }
                return routes.map((label, i) =>
                    String(label || '').trim() || 'output_' + (i + 1));
            },

            /**
             * Number of output ports a checkpoint node needs: one per
             * declared route (each route releases onto its own edge).
             */
            checkpointOutputCount(data) {
                return Math.max(1, this.checkpointRouteLabels(data).length);
            },

            /**
             * Node types supporting the opt-in errorCatch flag
             * (task error_catch_port): the engine appends a
             * dedicated "error" port after the normal outputs.
             */
            supportsErrorCatch(typeId) {
                return {
                    httpRequest: true, exec: true, tcpRequest: true,
                    file: true, fileIn: true, mailOut: true, db: true
                }[typeId] === true;
            },

            /**
             * Whether the node's error port is enabled AND present:
             * the flag must be on for a supported type.
             */
            errorCatchEnabled(typeId, data) {
                return this.supportsErrorCatch(typeId) &&
                    (data && (data.errorCatch === true || data.errorCatch === 'true'));
            },

            /**
             * Number of output ports an errorCatch-capable node needs:
             * the type's normal outputs plus the trailing "error" port
             * when enabled (appended after, so existing port indices
             * never shift).
             */
            errorCatchOutputCount(type, data) {
                return (type.outputs || 0) + (this.errorCatchEnabled(type.id, data) ? 1 : 0);
            },

            /**
             * Per-output labels for an errorCatch node: the normal
             * ports keep their labels (default labels when none were
             * persisted), the appended port is labelled "error".
             */
            errorCatchOutputLabels(type, data) {
                const base = type.outputs || 0;
                let labels = Array.isArray(data && data.outputs)
                    ? data.outputs.slice(0, base) : this.defaultOutputLabels(base);
                while (labels.length < base) labels.push('');
                if (this.errorCatchEnabled(type.id, data)) labels.push('error');
                return labels;
            },

            /**
             * Desired output-port count for node types whose ports are
             * config-driven (the "outputsFrom" convention). Returns null
             * for fixed-port types so saveNodeProperties can call
             * syncDynamicOutputs unconditionally. New dynamic-port types
             * hook in here (plus a <type>OutputLabels for data.outputs).
             */
            dynamicOutputCount(node) {
                const typeId = node && node.data && node.data.type;
                const data = (node && node.data) || {};
                if (typeId === 'switch') return this.switchOutputCount(data);
                if (typeId === 'checkpoint') return this.checkpointOutputCount(data);
                // delay: outputs:2 adds the burst port for dropped
                // rate-limiter messages (numeric config, not labels).
                if (typeId === 'delay') return Number(data.outputs) === 2 ? 2 : 1;
                if (this.supportsErrorCatch(typeId)) {
                    const type = this.findType(typeId) || { id: typeId, outputs: 1 };
                    return this.errorCatchOutputCount(type, data);
                }
                return null;
            },

            /**
             * Resize a node's output ports to match its config-driven
             * count (switch rules + otherwise, checkpoint routes,
             * errorCatch flag). Connections on ports beyond the new
             * count are dropped first with a console note (Node-RED
             * behaviour: dangling wires removed); Drawflow's
             * addNodeOutput / removeNodeOutput do the DOM/graph
             * surgery in place so the canvas updates without reload.
             */
            syncDynamicOutputs(nodeId) {
                const node = this.editor.getNodeFromId(nodeId);
                if (!node) return;
                const desired = this.dynamicOutputCount(node);
                if (desired === null || desired === undefined) return;
                const current = Object.keys(node.outputs || {}).length;
                if (desired === current) {
                    this.refreshOutputLabels(nodeId);
                    return;
                }
                if (typeof this.editor.addNodeOutput !== 'function' ||
                    typeof this.editor.removeNodeOutput !== 'function') {
                    console.error('syncDynamicOutputs: Drawflow build lacks addNodeOutput/removeNodeOutput');
                    if (typeof this.showToast === 'function') {
                        this.showToast('Could not update node ports — save and reload the page');
                    }
                    return;
                }
                let dropped = 0;
                Object.keys(node.outputs || {}).forEach(outputKey => {
                    const idx = parseInt(String(outputKey).replace('output_', ''), 10);
                    if (idx > desired) {
                        ((node.outputs[outputKey] && node.outputs[outputKey].connections) || [])
                            .forEach(c => {
                                this.editor.removeSingleConnection(
                                    nodeId, c.node, outputKey, c.output);
                                dropped++;
                            });
                    }
                });
                if (dropped > 0) {
                    console.info('syncDynamicOutputs: dropped ' + dropped +
                        ' wire(s) on removed output port(s) of node ' + nodeId);
                }
                for (let i = current + 1; i <= desired; i++) this.editor.addNodeOutput(nodeId);
                for (let i = current; i > desired; i--) this.editor.removeNodeOutput(nodeId, 'output_' + i);
                this.refreshOutputLabels(nodeId);
            },

            addNode(typeId, name, x, y, data) {
                // Unknown types (e.g. imported Node-RED nodes with no
                // engine equivalent) keep their raw id so the type
                // survives a save/load round-trip; they render with a
                // visual "unknown" marker instead of being retyped.
                const known = this.findType(typeId);
                const type = known || {
                    id: typeId, name: typeId, inputs: 1, outputs: 1,
                    color: '#6c757d'
                };
                const nodeData = Object.assign({ name, type: type.id }, data || {});
                // Switch output ports come from its rules + otherwise flag,
                // not the static type default
                const isSwitch = type.id === 'switch';
                const isCheckpoint = type.id === 'checkpoint';
                const hasErrorCatch = this.errorCatchEnabled(type.id, nodeData);
                // Checkpoint output ports come from its routes config
                // (routes[i] labels output_{i+1}), same model as switch;
                // errorCatch appends an "error" port after the normal
                // outputs (engine: error port = output_{normal+1}).
                const outputCount = isSwitch ? this.switchOutputCount(nodeData)
                    : isCheckpoint ? this.checkpointOutputCount(nodeData)
                    : hasErrorCatch ? this.errorCatchOutputCount(type, nodeData)
                    : type.outputs;
                // Persist default per-output labels for multi-output nodes so
                // they survive export/import through node.data
                if (hasErrorCatch && !isSwitch && !isCheckpoint) {
                    nodeData.outputs = this.errorCatchOutputLabels(type, nodeData);
                } else if (outputCount > 1 && !Array.isArray(nodeData.outputs)) {
                    nodeData.outputs = isSwitch
                        ? this.switchOutputLabels(nodeData)
                        : isCheckpoint
                            ? this.checkpointRouteLabels(nodeData)
                            : this.defaultOutputLabels(outputCount);
                }
                // Component port markers: assign the next port index
                // within the current child flow when none is given, so the
                // serialized in[]/out[] order is stable regardless of
                // where the marker is dropped.
                if (isComponentPortClass(type.id) &&
                    (nodeData.portIndex === undefined || nodeData.portIndex === null)) {
                    const flow = (this.editor.drawflow.drawflow || {})[this.editor.module];
                    let maxPort = -1;
                    Object.keys((flow && flow.data) || {}).forEach(k => {
                        const n = flow.data[k];
                        if (n && n.class === type.id && n.data &&
                            typeof n.data.portIndex === 'number' && n.data.portIndex > maxPort) {
                            maxPort = n.data.portIndex;
                        }
                    });
                    nodeData.portIndex = maxPort + 1;
                }
                const summary = this.nodeSummary(nodeData);
                // Inject nodes get a manual "fire" button in the node body
                const fireButton = type.id === 'inject'
                    ? `<button class="inject-fire-btn btn btn-sm btn-outline-success mt-1" title="Fire inject">
                           <i class="fas fa-bolt"></i>
                       </button>`
                    : '';
                // Queue/checkpoint nodes: depth badge (updated by the
                // status poll) plus a click-to-inspect button opening
                // the shared inspection panel.
                const queueBits = (type.id === 'queue' || type.id === 'checkpoint')
                    ? `<div class="queue-meta mt-1">
                           <span class="queue-depth badge text-bg-secondary" title="Queued items">0</span>
                           <button class="queue-inspect-btn btn btn-sm btn-outline-secondary ms-1" title="Inspect queue">
                               <i class="fas fa-inbox"></i>
                           </button>
                       </div>`
                    : '';
                const html = `
                    <div class="flow-node ${type.id}${known ? '' : ' flow-node-unknown'}">
                        <div class="node-header" style="background:${type.color}">${this.escapeHtml(nodeData.name)}</div>
                        <div class="node-body">
                            <div>Type: ${this.escapeHtml(type.id)}${known ? '' : ' <span class="badge text-bg-secondary">unknown</span>'}</div>
                            <div class="node-summary">${summary}</div>
                            ${fireButton}
                            ${queueBits}
                        </div>
                    </div>`;
                return this.editor.addNode(nodeData.name, type.inputs, outputCount, x, y, type.id, nodeData, html, false);
            },

            /**
             * Refresh the node header + summary in the DOM after a data change
             */
            refreshNodeDom(nodeId, data) {
                const el = document.getElementById('node-' + nodeId);
                if (!el) return;
                const header = el.querySelector('.node-header');
                if (header) header.textContent = data.name;
                const summaryEl = el.querySelector('.node-summary');
                if (summaryEl) summaryEl.innerHTML = this.nodeSummary(data);
                this.refreshOutputLabels(nodeId);
            },

            /**
             * Default labels for a node type with the given output count
             * (true/false for binary branches, numbered otherwise)
             */
            defaultOutputLabels(count) {
                if (count === 2) return ['true', 'false'];
                const labels = [];
                for (let i = 1; i <= count; i++) labels.push(String(i));
                return labels;
            },

            /**
             * Inject a label span next to each output port of a node. Labels
             * come from node.data.outputs (persisted through export/import).
             */
            refreshOutputLabels(nodeId) {
                const el = document.getElementById('node-' + nodeId);
                if (!el) return;
                const node = this.editor.getNodeFromId(nodeId);
                const data = (node && node.data) || {};
                const labels = Array.isArray(data.outputs) ? data.outputs : [];
                el.querySelectorAll('.outputs .output').forEach((outputEl, i) => {
                    let labelEl = outputEl.querySelector('.output-label');
                    const text = labels[i];
                    if (!text) {
                        if (labelEl) labelEl.remove();
                        return;
                    }
                    if (!labelEl) {
                        labelEl = document.createElement('span');
                        labelEl.className = 'output-label';
                        outputEl.appendChild(labelEl);
                    }
                    labelEl.textContent = text;
                });
            },

            /**
             * Re-apply output labels to every node (e.g. after editor.import)
             */
            refreshAllOutputLabels() {
                const flow = (this.editor.drawflow && this.editor.drawflow.drawflow &&
                    this.editor.drawflow.drawflow[this.editor.module] &&
                    this.editor.drawflow.drawflow[this.editor.module].data) || {};
                Object.keys(flow).forEach(id => this.refreshOutputLabels(id));
            },

            /* ---- Node groups (task 36/39): model lives in floweditor ---- */

            /**
             * Groups as they are persisted in workflow.view.groups: a
             * flat array carrying the child flow name, flattened from the
             * editor's per-child flow model. Members are filtered to live
             * nodes so a phantom group never renders as a box at (0,0).
             */
            serializeGroups() {
                const out = [];
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                Object.keys(df).forEach(flow => {
                    const data = df[flow].data || {};
                    Object.values(df[flow].groups || {}).forEach(g => {
                        const nodeIds = (g.nodeIds || []).filter(id => data[id]);
                        if (!nodeIds.length) return;
                        const entry = {
                            id: g.id,
                            // Child flow id, matching node data.childFlow.
                            childFlow: this.flowIdFor(flow),
                            name: g.name,
                            x: g.x, y: g.y, w: g.w, h: g.h,
                            nodeIds: nodeIds,
                            // Group env (env.get resolves it first)
                            env: Array.isArray(g.env) ? g.env : []
                        };
                        // Disabled groups disable every member node
                        // (resolved at runtime by the engine).
                        if (g.disabled === true) entry.disabled = true;
                        out.push(entry);
                    });
                });
                return out;
            },

            /**
             * Resolve a stored group member id to a live node id in the
             * group's child flow (kept for e2e coverage; ids are unified
             * now that groups live in the editor model).
             */
            memberDrawflowId(gid) {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                for (const flow of Object.keys(df)) {
                    const groups = df[flow].groups || {};
                    for (const g of Object.values(groups)) {
                        if ((g.nodeIds || []).indexOf(String(gid)) !== -1) {
                            return (df[flow].data || {})[gid] ? String(gid) : null;
                        }
                    }
                }
                return null;
            },

            /**
             * Ctrl/meta-click multi-selection used by "Group selected"
             */
            toggleNodeSelection(dfId) {
                const id = String(dfId);
                if (this.editor.multiselect) {
                    // The library owns the visual state ('multiselected'
                    // class); selectedNodeIds follows 'selectionChanged'
                    const ids = this.editor.getSelectedNodes().map(String);
                    const i = ids.indexOf(id);
                    if (i !== -1) ids.splice(i, 1); else ids.push(id);
                    this.editor.selectNodes(ids);
                    return;
                }
                const el = document.getElementById('node-' + id);
                const idx = this.selectedNodeIds.indexOf(id);
                if (idx !== -1) {
                    this.selectedNodeIds.splice(idx, 1);
                    if (el) el.classList.remove('flow-selected');
                } else {
                    this.selectedNodeIds.push(id);
                    if (el) el.classList.add('flow-selected');
                }
            },

            clearNodeSelection() {
                if (this.editor && this.editor.multiselect) {
                    this.editor.selectNodes([]);
                    return;
                }
                this.selectedNodeIds.forEach(id => {
                    const el = document.getElementById('node-' + id);
                    if (el) el.classList.remove('flow-selected');
                });
                this.selectedNodeIds = [];
            },

            /**
             * Members for a group create: the multi-selection when it
             * contains the right-clicked node, otherwise that node alone.
             */
            groupSelectedNodes() {
                const cmId = this.contextMenu.nodeId;
                // Canvas clicks clear the library selection without firing
                // 'selectionChanged', so read it live instead of trusting
                // the synced list
                const selected = (this.editor && this.editor.multiselect)
                    ? this.editor.getSelectedNodes().map(String)
                    : this.selectedNodeIds;
                const members = (cmId && selected.indexOf(String(cmId)) !== -1)
                    ? selected.slice()
                    : (cmId ? [String(cmId)] : []);
                if (!members.length) return;
                this.editor.addGroup('group', members);
                this.clearNodeSelection();
            },

            /**
             * True when the node id belongs to a group in the active
             * child flow — drives context menu visibility.
             */
            nodeInAnyGroup(dfId) {
                const id = String(dfId);
                return this.editor.groupList(this.editor.module)
                    .some(g => (g.nodeIds || []).indexOf(id) !== -1);
            },

            /**
             * Remove the group containing a node without touching the
             * nodes or edges themselves.
             */
            ungroupNode(nodeId) {
                const id = String(nodeId);
                const g = this.editor.groupList(this.editor.module)
                    .find(g => (g.nodeIds || []).indexOf(id) !== -1);
                if (g) this.editor.removeGroup(g.id);
            },

            /**
             * Properties modal for a group (label double-click). The
             * member list resolves live node labels via getNodeFromId.
             */
            openGroupProps(group) {
                if (!group) return;
                this.groupEdit = {
                    id: group.id,
                    name: group.name || '',
                    disabled: group.disabled === true,
                    members: (group.nodeIds || []).map(id => {
                        const n = this.editor.getNodeFromId(id);
                        const label = (n && n.data && (n.data.name || n.data.label)) || (n && n.name) || id;
                        return { id: String(id), label: label };
                    })
                };
                bootstrap.Modal.getOrCreateInstance(this.$refs.groupPropsModal).show();
            },

            /**
             * Save the group properties modal. updateGroup re-renders
             * the label and fires groupUpdated (dirty + history); the
             * disabled flag lives on the group model and persists
             * through view.groups — updateGroup does not know the key
             * on this floweditor build, so it is set directly like
             * toggleGroupDisabled does.
             */
            saveGroupProps() {
                const edit = this.groupEdit;
                if (!edit.id) return;
                const flow = this.editor.drawflow.drawflow[this.editor.module];
                const g = flow && flow.groups ? flow.groups[edit.id] : null;
                if (g) g.disabled = edit.disabled === true;
                this.editor.updateGroup(edit.id, { name: edit.name });
                this.refreshDisabledDom();
                bootstrap.Modal.getInstance(this.$refs.groupPropsModal).hide();
            },

            /* ---- Disabled nodes / tabs / groups (Node-RED "d" parity) ---- */

            /**
             * True when node data carries a disabled flag — Node-RED's
             * "d": true or the long-form "disabled".
             */
            isNodeDisabledData(data) {
                return !!(data && (data.d === true || data.disabled === true));
            },

            isNodeDisabled(nodeId) {
                const node = this.editor.getNodeFromId(nodeId);
                return this.isNodeDisabledData(node && node.data);
            },

            /**
             * Dim a disabled node (Node-RED renders disabled nodes greyed).
             */
            setNodeDisabledDom(nodeId, disabled) {
                const el = document.getElementById('node-' + nodeId);
                if (el) el.classList.toggle('flow-node-disabled', !!disabled);
            },

            /**
             * Recompute the dimmed state of every node in every childFlow:
             * a node dims when it is disabled itself, sits on a disabled
             * tab, or belongs to a disabled group.
             */
            refreshDisabledDom() {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                Object.keys(df).forEach(flow => {
                    // Closed childFlows have no tab — their disabled state
                    // comes straight from meta.enabled (the tree toggle
                    // and applyView both write it there).
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? this.editor.getModuleMeta(flow) : null;
                    const tabDisabled = this.flowTabs.some(t => t.childFlow === flow && t.disabled) ||
                        (meta && meta.enabled === false);
                    const disabledGroups = Object.values(df[flow].groups || {})
                        .filter(g => g.disabled === true);
                    const data = df[flow].data || {};
                    Object.keys(data).forEach(id => {
                        let dis = tabDisabled || this.isNodeDisabledData(data[id].data);
                        if (!dis) {
                            dis = disabledGroups.some(g =>
                                (g.nodeIds || []).map(String).indexOf(String(id)) !== -1);
                        }
                        this.setNodeDisabledDom(id, dis);
                    });
                });
            },

            /**
             * Context-menu enable/disable toggle for a single node:
             * sets node.data.d (removed entirely when re-enabled).
             */
            toggleNodeDisabled(nodeId) {
                const node = this.editor.getNodeFromId(nodeId);
                if (!node) return;
                const data = Object.assign({}, node.data || {});
                const disabled = !this.isNodeDisabledData(data);
                if (disabled) {
                    data.d = true;
                } else {
                    delete data.d;
                    delete data.disabled;
                }
                this.editor.updateNodeDataFromId(nodeId, data);
                this.refreshNodeDom(nodeId, data);
                this.refreshDisabledDom();
                this.dirty = true;
                this.pushHistory();
            },

            /**
             * The group containing a node in the active child flow (or null).
             */
            nodeGroup(nodeId) {
                const id = String(nodeId);
                return this.editor.groupList(this.editor.module)
                    .find(g => (g.nodeIds || []).indexOf(id) !== -1) || null;
            },

            nodeGroupDisabled(nodeId) {
                const g = this.nodeGroup(nodeId);
                return !!(g && g.disabled === true);
            },

            /**
             * Context-menu toggle for the whole group containing a node.
             * The flag lives on the group model and persists through
             * view.groups — the engine resolves membership at runtime.
             */
            toggleGroupDisabled(nodeId) {
                const g = this.nodeGroup(nodeId);
                if (!g) return;
                const disabled = !(g.disabled === true);
                // Mutate the model directly; updateGroup may not know
                // the disabled key on older floweditor builds.
                const flow = this.editor.drawflow.drawflow[this.editor.module];
                if (flow && flow.groups && flow.groups[g.id]) flow.groups[g.id].disabled = disabled;
                g.disabled = disabled;
                if (typeof this.editor.updateGroup === 'function') {
                    try { this.editor.updateGroup(g.id, { disabled: disabled }); } catch (e) { /* optional */ }
                }
                this.refreshDisabledDom();
                this.dirty = true;
                this.pushHistory();
            },

            /* ---- Env tables (env.get chain: group > component > flow > global > OS) ---- */

            /**
             * Deep-copy an env row list so modal edits stay uncommitted
             * until Save.
             */
            envRowsClone(rows) {
                return (rows || []).map(r => ({
                    name: r.name || '',
                    type: r.type || 'str',
                    value: r.value !== undefined && r.value !== null ? String(r.value) : ''
                }));
            },

            /**
             * Open the shared env editor for this workflow's flow-level
             * env (persisted as view.env).
             */
            openFlowEnv() {
                if (!this.selectedWorkflow) return;
                this.envEdit = {
                    target: 'flow',
                    groupId: null,
                    title: 'Flow Environment Variables',
                    rows: this.envRowsClone(this.flowEnv)
                };
                bootstrap.Modal.getOrCreateInstance(this.$refs.envModal).show();
            },

            /**
             * Open the shared env editor for the deployment-wide
             * global-config env (a server-side singleton).
             */
            async openGlobalEnv() {
                let rows = [];
                try {
                    const data = await this.api('/api/global-env-fetch');
                    rows = Array.isArray(data.env) ? data.env : [];
                } catch (err) {
                    this.showToast('Global env load failed: ' + err.message, 'danger');
                    return;
                }
                this.envEdit = {
                    target: 'global',
                    groupId: null,
                    title: 'Global Environment Variables',
                    rows: this.envRowsClone(rows)
                };
                bootstrap.Modal.getOrCreateInstance(this.$refs.envModal).show();
            },

            /**
             * Open the shared env editor for the group containing the
             * given node (group env outranks flow env in env.get).
             */
            openGroupEnv(nodeId) {
                const id = String(nodeId);
                const g = this.editor.groupList(this.editor.module)
                    .find(g => (g.nodeIds || []).indexOf(id) !== -1);
                if (g) this.openGroupEnvById(g.id);
            },

            /**
             * Same as openGroupEnv but resolves the group directly by
             * id across childFlows — used by the group properties modal.
             */
            openGroupEnvById(groupId) {
                const df = this.editor.drawflow.drawflow || {};
                let g = null;
                for (const flow of Object.keys(df)) {
                    g = (df[flow].groups || {})[groupId];
                    if (g) break;
                }
                if (!g) return;
                this.envEdit = {
                    target: 'group',
                    groupId: g.id,
                    title: 'Group Environment Variables — ' + (g.name || g.id),
                    rows: this.envRowsClone(g.env)
                };
                bootstrap.Modal.getOrCreateInstance(this.$refs.envModal).show();
            },

            /**
             * Open the shared env editor for a role:"component" childFlow:
             * the rows persist as meta.env and serialize into the
             * definition's env[] on save (the env.get chain's component
             * level).
             */
            openComponentEnv(flowKey) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? (this.editor.getModuleMeta(flowKey) || {}) : {};
                this.envEdit = {
                    target: 'childflow',
                    groupId: null,
                    flowKey: flowKey,
                    title: 'Component Environment Variables — ' + (meta.label || flowKey),
                    rows: this.envRowsClone(meta.env)
                };
                bootstrap.Modal.getOrCreateInstance(this.$refs.envModal).show();
            },

            addEnvRow() {
                this.envEdit.rows.push({ name: '', type: 'str', value: '' });
            },

            removeEnvRow(i) {
                this.envEdit.rows.splice(i, 1);
            },

            /**
             * Commit the modal rows to their target: flow env marks the
             * workflow dirty (saved with the flow), group env writes the
             * editor group model, global env persists immediately via
             * the API.
             */
            async saveEnvModal() {
                const rows = (this.envEdit.rows || [])
                    .filter(r => (r.name || '').trim() !== '')
                    .map(r => ({ name: r.name.trim(), type: r.type || 'str', value: r.value }));
                const edit = this.envEdit;
                bootstrap.Modal.getInstance(this.$refs.envModal).hide();
                if (edit.target === 'flow') {
                    this.flowEnv = rows;
                    this.dirty = true;
                    this.pushHistory();
                    return;
                }
                if (edit.target === 'group') {
                    const df = this.editor.drawflow.drawflow || {};
                    for (const flow of Object.keys(df)) {
                        const g = (df[flow].groups || {})[edit.groupId];
                        if (g) {
                            g.env = rows;
                            this.dirty = true;
                            this.pushHistory();
                            break;
                        }
                    }
                    return;
                }
                if (edit.target === 'childflow') {
                    // Component-child flow env lives on meta.env and serializes
                    // into the definition's env[] on save.
                    if (typeof this.editor.setModuleMeta === 'function') {
                        this.editor.setModuleMeta(edit.flowKey, { env: rows });
                        this.dirty = true;
                        this.pushHistory();
                    }
                    return;
                }
                if (edit.target === 'global') {
                    try {
                        await this.api('/api/global-env-update', { env: rows });
                        this.showToast('Global env saved', 'info');
                    } catch (err) {
                        this.showToast('Global env save failed: ' + err.message, 'danger');
                    }
                }
            },

            /**
             * Snapshot of the current canvas zoom/pan for persistence
             */
            currentView() {
                return {
                    zoom: this.editor.zoom,
                    canvas_x: this.editor.canvas_x,
                    canvas_y: this.editor.canvas_y,
                    groups: this.serializeGroups(),
                    // Child flow entities {id,label,role,enabled,env} — the
                    // stable ids node data.childFlow and the references
                    // below point at.
                    childFlows: this.serializeChildFlows(),
                    // Disabled childFlows (Node-RED "disabled": true on
                    // the tab object): child flow ids whose nodes deploy
                    // inert (legacy documents stored names; the engine
                    // resolves both through view.childFlows). Covers closed
                    // childFlows too — their flag lives on meta.enabled.
                    disabledFlows: Object.keys(this.editor.drawflow.drawflow || {})
                        .filter(m => {
                            const tab = this.flowTabs.find(t => t.childFlow === m);
                            const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                                ? this.editor.getModuleMeta(m) : null;
                            return (tab && tab.disabled === true) ||
                                (meta && meta.enabled === false);
                        })
                        .map(m => this.flowIdFor(m)),
                    // Open-tab set (child flow navigator): child flow ids of the
                    // childFlows with a tab, so a close survives reload.
                    openFlows: this.flowTabs.map(t => this.flowIdFor(t.childFlow)),
                    // Sidebar folders (sidebar_file_tree): pure
                    // organization state — [{id,name,parentId,
                    // childFlows:[childFlowId]}]; carries no runtime meaning.
                    treeGroups: this.serializeTreeGroups()
                };
            },

            /**
             * Restore a saved zoom/pan view state (if present)
             */
            applyView(view) {
                // Sidebar folder state (view.treeGroups): a flat list
                // of {id,name,parentId,childFlows:[childFlowId]}. Missing or
                // malformed entries degrade to the ungrouped flat
                // list; stale child flow refs are skipped at render.
                const tg = (view && Array.isArray(view.treeGroups)) ? view.treeGroups : [];
                this.treeGroups = tg
                    .filter(g => g && g.id)
                    .map(g => ({
                        id: String(g.id),
                        name: String(g.name || 'Folder'),
                        parentId: g.parentId ? String(g.parentId) : null,
                        childFlows: Array.isArray(g.childFlows) ? g.childFlows.map(String) : []
                    }));
                this.collapsedTreeNodes = {};
                this.folderContextMenu.show = false;
                // Migrate persisted view.groups (flat array, child flow
                // field) into the editor's per-child flow group model.
                // Members that no longer resolve are dropped; a group
                // left with no members is skipped entirely.
                const saved = (view && Array.isArray(view.groups)) ? view.groups : [];
                const df = this.editor.drawflow.drawflow || {};
                saved.forEach(g => {
                    // g.childFlow stores a child flow id (or a legacy name).
                    const flow = this.resolveFlowName(g.childFlow || 'Home');
                    const m = df[flow];
                    if (!m) return;
                    if (!m.groups) m.groups = {};
                    const nodeIds = (g.nodeIds || []).map(String).filter(id => (m.data || {})[id]);
                    if (!nodeIds.length) return;
                    m.groups[g.id] = {
                        id: g.id,
                        name: g.name || 'group',
                        nodeIds: nodeIds,
                        x: g.x !== undefined ? g.x : null,
                        y: g.y !== undefined ? g.y : null,
                        w: g.w !== undefined ? g.w : null,
                        h: g.h !== undefined ? g.h : null,
                        env: Array.isArray(g.env) ? g.env : [],
                        disabled: g.disabled === true
                    };
                });
                this.editor.renderGroups();
                // Disabled flow tabs persist as view.disabledFlows
                // (child flow ids; legacy documents stored names) — restore
                // the flag onto the tab model. A child flow entity with
                // enabled:false disables the tab too.
                const dm = (view && Array.isArray(view.disabledFlows))
                    ? view.disabledFlows.map(v => this.resolveFlowName(String(v))) : [];
                // Apply to every child flow, open or closed: tabs carry a
                // disabled flag for the styling, meta.enabled is the
                // persisted flag the tree and deploy read.
                Object.keys(this.editor.drawflow.drawflow || {}).forEach(m => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? this.editor.getModuleMeta(m) : null;
                    const disabled = dm.indexOf(m) !== -1 ||
                        (meta && meta.enabled === false);
                    const tab = this.flowTabs.find(t => t.childFlow === m);
                    if (tab) tab.disabled = disabled;
                    if (typeof this.editor.setModuleMeta === 'function') {
                        this.editor.setModuleMeta(m, { enabled: !disabled });
                    }
                });
                this.refreshDisabledDom();
                if (!view || typeof view.zoom !== 'number') return;
                this.editor.zoom = view.zoom;
                this.editor.zoom_last_value = view.zoom;
                this.editor.canvas_x = view.canvas_x || 0;
                this.editor.canvas_y = view.canvas_y || 0;
                this.editor.zoom_refresh();
            },

            /**
             * Transient user feedback toast (top-right, auto-dismisses)
             */
            showToast(message, type) {
                const toast = document.createElement('div');
                toast.className = 'flow-toast alert alert-' + (type || 'warning') + ' py-2 px-3 shadow';
                toast.textContent = message;
                document.body.appendChild(toast);
                setTimeout(() => toast.remove(), 3500);
            },

            /* ---- Components (role:"component" childFlows + "component:<id>" instances) ---- */

            /**
             * Create a component definition as a role:"component" canvas
             * child flow (component_child_flow_tree) and open it in a tab. The
             * default inner graph is one input marker wired through a
             * passthrough junction to one output marker, so an instance
             * works out of the box; the user edits the inner graph and
             * ports right on the canvas.
             */
            createComponent() {
                if (!this.selectedWorkflow) return;
                const name = prompt('Component name', 'Component');
                if (name === null) return;
                const df = this.editor.drawflow.drawflow || {};
                const base = String(name || '').trim() || 'Component';
                let key = base;
                for (let i = 2; df[key]; i++) key = base + ' ' + i;
                this.editor.addModule(key);
                const defId = uuidV7();
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(key, {
                        id: defId,
                        label: base,
                        role: 'component',
                        enabled: true,
                        env: []
                    });
                }
                this.editor.changeModule(key);
                const inId = this.addNode('componentIn', 'input 1', 40, 40,
                    { name: 'input 1', portIndex: 0 });
                const innerId = this.addNode('junction', 'passthrough', 220, 40,
                    { name: 'passthrough' });
                const outId = this.addNode('componentOut', 'output 1', 420, 40,
                    { name: 'output 1', portIndex: 0 });
                try {
                    this.editor.addConnection(inId, innerId, 'output_1', 'input_1');
                    this.editor.addConnection(innerId, outId, 'output_1', 'input_1');
                } catch (err) { /* default wiring is best-effort */ }

                const tab = { childFlow: key, label: base, role: 'component' };
                this.flowTabs.push(tab);
                this.knownFlows.push(key);
                this.flowTreeVersion++;
                this.switchTab(tab);
                this.pushHistory();
                this.dirty = true;
                this.showToast('Component created — it now appears in Add Node; Save to persist', 'info');
                return key;
            },

            /**
             * Copy the workflow as a Node-RED flow JSON array (the same
             * shape Import accepts). Falls back to the raw Drawflow
             * export when the conversion endpoint is unavailable.
             */
            async exportFlowJson() {
                let json = null;
                if (this.selectedWorkflow) {
                    try {
                        if (this.dirty) await this.saveWorkflow();
                        const data = await this.api('/api/workflow-export-nodered', { id: this.selectedWorkflow.id });
                        if (data.flow) json = JSON.stringify(data.flow, null, 2);
                    } catch (err) {
                        console.warn('Node-RED export failed, falling back to Drawflow JSON:', err.message);
                    }
                }
                // Credentials never leave in exports — the server strips
                // them too; this covers the raw Drawflow fallback path.
                if (!json) json = JSON.stringify(this.scrubCredentials(this.editor.export()), null, 2);
                if (navigator.clipboard && navigator.clipboard.writeText) {
                    navigator.clipboard.writeText(json)
                        .then(() => this.setSaveStatus('saved', 'Flow JSON copied to clipboard'))
                        .catch(() => this.fallbackCopyJson(json));
                } else {
                    this.fallbackCopyJson(json);
                }
            },

            /**
             * Clipboard fallback for non-secure contexts
             */
            fallbackCopyJson(text) {
                const ta = document.createElement('textarea');
                ta.value = text;
                ta.style.position = 'fixed';
                ta.style.opacity = '0';
                document.body.appendChild(ta);
                ta.select();
                try {
                    document.execCommand('copy');
                    this.setSaveStatus('saved', 'Flow JSON copied to clipboard');
                } catch (err) {
                    this.showToast('Copy failed — use Import to paste JSON instead', 'danger');
                }
                ta.remove();
            },

            /**
             * Open the Import modal
             */
            openImportModal() {
                this.importJson = '';
                this.importError = '';
                bootstrap.Modal.getOrCreateInstance(this.$refs.importModal).show();
            },

            /**
             * Parse the pasted JSON and load it into the editor
             */
            confirmImport() {
                let parsed;
                try {
                    parsed = JSON.parse(this.importJson);
                } catch (err) {
                    this.importError = 'Invalid JSON: ' + err.message;
                    return;
                }
                // A top-level array is a Node-RED flow export — hand it
                // to the backend converter (component definitions and
                // instances come along) and open the imported workflow.
                if (Array.isArray(parsed)) {
                    this.importNodeRedFlow(parsed);
                    return;
                }
                // Drawflow-JSON imports target the open editor — with no
                // workflow selected there is none.
                // The Node-RED path above is the empty-state import.
                if (!this.selectedWorkflow) {
                    this.importError = 'Drawflow JSON needs an open workflow — paste a Node-RED flow export (JSON array) to create one, or create/select a workflow first';
                    return;
                }
                this.pushHistory();
                try {
                    this.editor.import(parsed);
                } catch (err) {
                    this.importError = 'Import failed: ' + err.message;
                    // Roll back the history entry we just pushed
                    this.undoStack.pop();
                    this.historySnapshot = this.snapshotNow();
                    return;
                }
                this.syncTabsFromEditor();
                this.refreshAllOutputLabels();
                this.scheduleMinimap();
                this.dirty = true;
                this.setSaveStatus('saved', 'Imported — press Save to persist');
                bootstrap.Modal.getInstance(this.$refs.importModal).hide();
            },

            /**
             * Import a Node-RED flow JSON array via the backend
             * converter; the result is a brand-new workflow that is
             * opened immediately.
             */
            async importNodeRedFlow(items) {
                try {
                    const data = await this.api('/api/workflow-import-nodered', { items: items });
                    await this.loadWorkflows();
                    bootstrap.Modal.getInstance(this.$refs.importModal).hide();
                    if (data.workflow) {
                        this.selectWorkflow(data.workflow);
                        this.showToast('Node-RED flow imported', 'success');
                    }
                } catch (err) {
                    this.importError = 'Import failed: ' + err.message;
                }
            },

            /**
             * floweditor connection_validator: runs before a wire is
             * committed (mouse drop and addConnection). Returning false
             * blocks the connection entirely — the wire never enters the
             * model, so no cleanup or history entry is needed. Toasts are
             * suppressed while renderFlow/undo replays stored edges
             * (suppressHistory), but invalid persisted edges are still
             * rejected.
             */
            validateNewConnection(info) {
                const srcNode = this.editor.getNodeFromId(info.output_id);
                const dstNode = this.editor.getNodeFromId(info.input_id);
                if (!srcNode || !dstNode) return false;

                const srcType = this.findType(srcNode.class || (srcNode.data && srcNode.data.type));
                const dstType = this.findType(dstNode.class || (dstNode.data && dstNode.data.type));

                let reason = null;
                if (dstType && dstType.inputs === 0) {
                    reason = 'Cannot connect into "' + dstNode.name + '" — this node type has no inputs';
                } else if (srcType && srcType.outputs === 0) {
                    reason = 'Cannot connect out of "' + srcNode.name + '" — this node type has no outputs';
                } else if (this.isDuplicateConnection(info)) {
                    reason = 'This connection already exists';
                } else if (this.wouldCreateCycle(info.output_id, info.input_id)) {
                    reason = 'Connection rejected — it would create a cycle';
                }

                if (reason) {
                    if (!this.suppressHistory) this.showToast(reason);
                    return false;
                }
                return true;
            },

            /**
             * The validator already rejected bad wires, so every
             * connectionCreated is a committed, valid connection
             */
            onConnectionCreated(info) {
                this.dirty = true;
                this.pushHistory();
            },

            /**
             * True if the new connection duplicates an existing port pair.
             * The validator runs before the edge is written, so a single
             * existing match already means a duplicate.
             */
            isDuplicateConnection(info) {
                const srcNode = this.editor.getNodeFromId(info.output_id);
                const output = srcNode && srcNode.outputs && srcNode.outputs[info.output_class];
                const matches = ((output && output.connections) || []).filter(c =>
                    String(c.node) === String(info.input_id) && c.output === info.input_class);
                return matches.length >= 1;
            },

            /**
             * True if the edge source->target closes a cycle (i.e. target can
             * already reach source through the current edge map)
             */
            wouldCreateCycle(sourceId, targetId) {
                const childFlowData = (this.editor.drawflow.drawflow[this.editor.module] || {}).data || {};
                const adjacency = {};
                Object.keys(childFlowData).forEach(id => {
                    adjacency[String(id)] = [];
                    Object.keys(childFlowData[id].outputs || {}).forEach(outputKey => {
                        (childFlowData[id].outputs[outputKey].connections || []).forEach(c => {
                            adjacency[String(id)].push(String(c.node));
                        });
                    });
                });
                const seen = {};
                const stack = [String(targetId)];
                while (stack.length) {
                    const cur = stack.pop();
                    if (cur === String(sourceId)) return true;
                    if (seen[cur]) continue;
                    seen[cur] = true;
                    (adjacency[cur] || []).forEach(next => stack.push(next));
                }
                return false;
            },

            /**
             * A dragged connection was released off a valid port (task 37,
             * floweditor 'connectionDrop' event). When the drop point is
             * empty canvas, open the Add Node modal as a quick-add picker;
             * the pending wire keeps the source port and the drop point
             * (already canvas world coordinates in the event payload).
             * Drops on a node, a port, or outside #drawflow are ignored.
             */
            onConnectionDrop(info) {
                if (!info || !this.selectedWorkflow) return;
                const hit = info.target;
                if (!hit || typeof hit.closest !== 'function' ||
                    !hit.closest('#drawflow') ||
                    hit.closest('.drawflow-node') ||
                    hit.closest('.input, .output')) {
                    return;
                }
                // Direction-aware (input-side drags, floweditor >= v0.9.x):
                // 'input' drags complete new-node-output → dragged input,
                // 'output' drags complete dragged output → new-node-input
                this.pendingWire = info.direction === 'input'
                    ? {
                        direction: 'input',
                        inputId: String(info.input_id),
                        inputClass: info.input_class,
                        x: info.x,
                        y: info.y
                    }
                    : {
                        direction: 'output',
                        outputId: String(info.output_id),
                        outputClass: info.output_class,
                        x: info.x,
                        y: info.y
                    };
                this.nodeTypeSearch = '';
                bootstrap.Modal.getOrCreateInstance(this.$refs.addNodeModal).show();
            },

            /**
             * Open the Add Node modal
             */
            openAddNodeModal() {
                this.pendingWire = null;
                this.nodeTypeSearch = '';
                bootstrap.Modal.getOrCreateInstance(this.$refs.addNodeModal).show();
            },

            /**
             * Add the selected node type at the canvas viewport center —
             * or, when a wire drag is pending, at the drop point with the
             * dragged wire completed into the new node's input_1 (task 37).
             */
            addNodeFromModal(nodeType) {
                const wire = this.pendingWire;
                this.pendingWire = null;
                if (wire) {
                    // suppressHistory so nodeCreated + connectionCreated do
                    // not each push an entry — a single push covers both,
                    // giving one undo step for "create + wire"
                    this.suppressHistory = true;
                    try {
                        const newId = this.addNode(nodeType.id, nodeType.name, wire.x - 80, wire.y - 30);
                        if (wire.direction === 'input') {
                            // Input-side drag: feed the dragged input from
                            // the new node's first output
                            this.editor.addConnection(newId, wire.inputId, 'output_1', wire.inputClass);
                        } else {
                            this.editor.addConnection(wire.outputId, newId, wire.outputClass, 'input_1');
                        }
                    } finally {
                        this.suppressHistory = false;
                    }
                    this.dirty = true;
                    this.pushHistory();
                } else {
                    const precanvas = this.editor.precanvas;
                    const zoom = this.editor.zoom;
                    const rect = precanvas.getBoundingClientRect();
                    const scale = precanvas.clientWidth / (precanvas.clientWidth * zoom);
                    const x = (rect.x + precanvas.clientWidth / 2) * scale - rect.x * scale;
                    const y = (rect.y + precanvas.clientHeight / 2) * scale - rect.y * scale;
                    this.addNode(nodeType.id, nodeType.name, x - 80, y - 30);
                }
                bootstrap.Modal.getInstance(this.$refs.addNodeModal).hide();
            },

            /**
             * Open the properties modal for the clicked node
             */
            /**
             * Pick a form field type for a JSON schema property
             */
            schemaFieldType(prop) {
                if (prop && (Array.isArray(prop.enum) || Array.isArray(prop.options))) return 'select';
                if (prop && prop.format === 'typedinput') return 'typedinput';
                // format:"code" — vendored CodeMirror editor with a
                // textarea fallback when the bundle did not load.
                if (prop && prop.format === 'code') return 'code';
                if (prop && prop.type === 'array' && prop.items && prop.items.properties) return 'items';
                switch (prop && prop.type) {
                    case 'boolean': return 'checkbox';
                    case 'number':
                    case 'integer': return 'number';
                    case 'object':
                    case 'array': return 'json';
                    case 'string':
                    default:
                        // Long/multiline strings get a textarea
                        if (prop && (prop.format === 'textarea' || prop.format === 'multiline')) return 'textarea';
                        return 'text';
                }
            },

            /**
             * Peer nodes of a link type across all childFlows — Node-RED's
             * link dialogs list every candidate in the flow.
             * Returns [{id, label}] sorted by label.
             */
            linkPeerNodes(peerType) {
                const out = [];
                const childFlows = (this.editor.export().drawflow) || {};
                Object.keys(childFlows).forEach(flow => {
                    const data = (childFlows[flow] && childFlows[flow].data) || {};
                    Object.keys(data).forEach(id => {
                        const n = data[id];
                        // data.type is the persisted type marker; class
                        // is Drawflow's internal fallback.
                        const type = (n.data && n.data.type) || n.class;
                        if (type !== peerType) return;
                        const label = (n.data && n.data.name) || (n.data && n.data.label) || id;
                        const backend = this.drawflowToBackend[String(id)] || String(id);
                        out.push({
                            id: backend,
                            label: flow === 'Home' ? label : label + ' [' + flow + ']'
                        });
                    });
                });
                out.sort((a, b) => a.label.localeCompare(b.label));
                return out;
            },

            /**
             * Enum options normalize to {value, label, description} —
             * rich selects render the label with its explanation beneath
             * (Bootstrap dropdown). Sources: prop.options
             * ([{value,title,description}|string]) or prop.enum +
             * prop.enumDescriptions {value: text}.
             */
            schemaOptions(prop) {
                const rawOpts = Array.isArray(prop.options) ? prop.options
                    : (Array.isArray(prop.enum) ? prop.enum : null);
                return rawOpts && rawOpts.map(o => {
                    if (o && typeof o === 'object') {
                        return {
                            value: o.value !== undefined ? o.value : o.title,
                            label: o.title || o.label || String(o.value),
                            description: o.description || ''
                        };
                    }
                    return {
                        value: o,
                        label: String(o),
                        description: (prop.enumDescriptions && prop.enumDescriptions[o]) || ''
                    };
                });
            },

            /**
             * Normalize one sub-property of an array item into a row
             * field {key,title,field,options,typeField,types,default,
             * defaultType} — the pieces an "items" row editor needs.
             * A format:"typedinput" field declares typeField: the
             * sibling item key holding the value's type (Node-RED vt).
             */
            schemaItemField(key, sub) {
                return {
                    key,
                    title: (sub && sub.title) || key,
                    description: (sub && sub.description) || '',
                    field: this.schemaFieldType(sub),
                    options: this.schemaOptions(sub || {}),
                    typeField: (sub && sub.typeField) || null,
                    types: (sub && sub.typedInputTypes) || ['msg', 'str', 'num', 'bool', 'json'],
                    default: sub && sub.default,
                    defaultType: (sub && sub.defaultType)
                        || (sub && sub.typedInputTypes && sub.typedInputTypes[0]) || 'str',
                    // Per-row visibility: {key: rowKey, in: [values]} —
                    // showWhen shows the sub-field only when the row's
                    // key value is in the list, hideWhen hides it then
                    // (switch rules hide the value for value-less ops).
                    showWhen: (sub && sub.showWhen) || null,
                    hideWhen: (sub && sub.hideWhen) || null
                };
            },

            /**
             * Sub-field visibility inside an "items" row: evaluates the
             * showWhen/hideWhen {key, in: [...]} clause against the
             * row's own values (compared as strings).
             */
            itemFieldVisible(sf, row) {
                if (!sf || !row) return true;
                const match = (clause) => clause && clause.key &&
                    Array.isArray(clause.in) &&
                    clause.in.map(String).indexOf(String(row[clause.key])) !== -1;
                if (sf.showWhen && !match(sf.showWhen)) return false;
                if (sf.hideWhen && match(sf.hideWhen)) return false;
                return true;
            },

            /**
             * Normalize a JSON schema into a flat field list for the form
             */
            buildSchemaFields(schema, nodeData) {
                const required = schema.required || [];
                const props = schema.properties || {};
                const fields = [];
                const form = {};
                Object.keys(props).forEach(key => {
                    const prop = props[key] || {};
                    let field = this.schemaFieldType(prop);
                    let options = this.schemaOptions(prop);
                    let itemFields = null;
                    let value = nodeData && nodeData[key] !== undefined ? nodeData[key]
                        : (prop.default !== undefined ? prop.default
                            : (field === 'checkbox' ? false : (field === 'json' ? {} : '')));

                    // Node-RED-style link pickers: checkboxes/select
                    // populated from live peer nodes, not typed ids.
                    if (prop.format === 'nodelist' && prop.peerType) {
                        field = 'nodelist';
                        options = this.linkPeerNodes(prop.peerType);
                        if (typeof value === 'string') {
                            value = value.split(',').map(s => s.trim()).filter(Boolean);
                        }
                        if (!Array.isArray(value)) value = [];
                        value = value.map(String);
                    } else if (prop.format === 'nodeselect' && prop.peerType) {
                        field = 'nodeselect';
                        options = this.linkPeerNodes(prop.peerType);
                        value = value == null ? '' : String(value);
                    } else if (field === 'items') {
                        // Node-RED-style row editor: each array element
                        // is an object whose sub-fields render inline.
                        const iprops = (prop.items && prop.items.properties) || {};
                        itemFields = Object.keys(iprops).map(k => this.schemaItemField(k, iprops[k]));
                        value = Array.isArray(value) ? JSON.parse(JSON.stringify(value)) : [];
                        // Fill missing typeField values on loaded rows.
                        value.forEach(row => {
                            itemFields.forEach(sf => {
                                if (sf.typeField && row[sf.typeField] === undefined) {
                                    row[sf.typeField] = sf.defaultType;
                                }
                            });
                        });
                    } else if (field === 'json') {
                        value = JSON.stringify(value, null, 2);
                    }

                    fields.push({
                        key,
                        title: prop.title || key,
                        description: prop.description || '',
                        field,
                        options,
                        itemFields,
                        // rich selects show each option's description
                        // under its label (Bootstrap dropdown)
                        rich: field === 'select' && !!options && options.some(o => o.description),
                        required: required.indexOf(key) !== -1,
                        // Conditional visibility (showIf / visible-when):
                        // {otherKey: expectedValue, ...} — the field is
                        // shown only when every listed form value equals
                        // its expected value (e.g. {backend: 'dir'}).
                        showIf: prop.showIf || prop['visible-when'] || null,
                        // format:"code" tab metadata — codeTab overrides
                        // the title for the tab strip (Node-RED "On
                        // Message" style short names); codeTabOrder
                        // sorts tabs (schema key order is alphabetical
                        // through the API, so order must be explicit).
                        codeTabLabel: prop.codeTab || null,
                        codeTabOrder: prop.codeTabOrder || 0
                    });
                    form[key] = value;
                });
                return { fields, form };
            },

            /**
             * showIf/visible-when evaluation: a field is visible when it
             * has no clause, or when every {key: expected} pair matches
             * the current form value (compared as strings so booleans
             * and numbers work too).
             */
            /**
             * Row editor helpers for "items" fields — add / remove /
             * reorder rows of {sub-field: value} objects (Node-RED's
             * rules-list pattern: switch rules, change rules, inject
             * props).
             */
            addItemRow(field) {
                if (!Array.isArray(this.editingNodeForm[field.key])) {
                    this.editingNodeForm[field.key] = [];
                }
                const row = {};
                (field.itemFields || []).forEach(sf => {
                    row[sf.key] = sf.default !== undefined ? sf.default
                        : (sf.field === 'checkbox' ? false : '');
                    if (sf.typeField) row[sf.typeField] = sf.defaultType;
                });
                this.editingNodeForm[field.key].push(row);
            },
            removeItemRow(field, i) {
                this.editingNodeForm[field.key].splice(i, 1);
            },
            moveItemRow(field, i, dir) {
                const a = this.editingNodeForm[field.key];
                const j = i + dir;
                if (!a || j < 0 || j >= a.length) return;
                a.splice(j, 0, a.splice(i, 1)[0]);
            },

            /**
             * Label shown on a rich-select dropdown button — the
             * selected option's label (or its raw value).
             */
            selectDisplayLabel(field) {
                const v = this.editingNodeForm[field.key];
                const opt = (field.options || []).find(o => String(o.value) === String(v));
                return opt ? opt.label : (v === '' || v === undefined ? '— select —' : String(v));
            },

            schemaFieldVisible(field) {
                if (!field || !field.showIf) return true;
                return Object.keys(field.showIf).every(k =>
                    String(this.editingNodeForm[k]) === String(field.showIf[k]));
            },

            /**
             * format:"code" tab helpers — every code field in the
             * schema is one tab of a single group (Node-RED On Start /
             * On Message / On Stop). Fields after the first render no
             * label; inactive tabs collapse to nothing.
             */
            isFirstCodeField(field) {
                // The tab bar + label anchor on the first code field in
                // ROW order (not tab order) so the bar always sits above
                // whichever field's editor is showing.
                const first = this.editingNodeSchemaFields.find(f => f.field === 'code');
                return !!first && first.key === field.key;
            },
            codeTabActive(field) {
                const cf = this.codeFields;
                if (!cf.length) return false;
                // No explicit selection yet: open on the required code
                // field (the function body — Node-RED's "On Message"),
                // else the first tab.
                const first = cf.find(f => f.required) || cf[0];
                const active = this.codeActiveTab || first.key;
                return active === field.key;
            },
            selectCodeTab(field) {
                this.codeActiveTab = field.key;
                // An editor created while its tab was hidden has a zero
                // size — refresh forces a re-measure on first show.
                this.$nextTick(() => {
                    const cm = this.codeEditorInst[field.key];
                    if (cm) cm.refresh();
                });
            },
            codeEditorActive(key) {
                return !!this.codeEditorOk[key];
            },

            /**
             * Create a CodeMirror per code-format field once the schema
             * form has rendered. Skips entirely when the vendored
             * bundle failed to load (window.CodeMirror absent) — the
             * bound textarea fallback stays visible and the value still
             * round-trips through editingNodeForm.
             */
            initCodeEditors() {
                if (typeof CodeMirror === 'undefined') return;
                this.$nextTick(() => {
                    const modal = this.$refs.nodePropsModal;
                    if (!modal) return;
                    const hosts = modal.querySelectorAll('.code-editor-host');
                    hosts.forEach(el => {
                        const key = el.getAttribute('data-code-field');
                        if (!key || this.codeEditorInst[key]) return;
                        try {
                            const cm = CodeMirror(el, {
                                value: this.editingNodeForm[key] || '',
                                mode: 'javascript',
                                lineNumbers: true,
                                indentUnit: 4,
                                tabSize: 4,
                                indentWithTabs: false
                            });
                            cm.setSize('100%', 300);
                            cm.on('change', () => {
                                this.editingNodeForm[key] = cm.getValue();
                            });
                            this.codeEditorInst[key] = cm;
                            this.codeEditorOk[key] = true;
                            cm.refresh();
                        } catch (e) {
                            // Any bundle/mode failure keeps the textarea
                            // fallback — nothing else to do.
                        }
                    });
                });
            },

            openNodeProperties(nodeId) {
                const node = this.editor.getNodeFromId(nodeId);
                if (!node) return;
                this.editingNodeId = nodeId;
                this.editingNodeType = node.data.type || node.name;
                this.editingNodeName = node.data.name || node.name;
                this.editingNodeDisabled = this.isNodeDisabledData(node.data);
                this.editingNodeData = JSON.stringify(node.data, null, 2);
                this.editingNodeDataError = '';
                this.editingNodeSchema = null;
                this.editingNodeSchemaFields = [];
                this.editingNodeForm = {};
                this.editingNodeFormErrors = {};
                // Drop stale code-editor state — the modal DOM is
                // re-rendered per open, so old CodeMirror wrappers are
                // gone with their host divs.
                this.codeEditorInst = {};
                this.codeEditorOk = {};
                this.codeActiveTab = '';
                bootstrap.Modal.getOrCreateInstance(this.$refs.nodePropsModal).show();

                // Try to fetch a JSON schema to drive a generated form;
                // on failure the modal falls back to the raw JSON textarea.
                this.api('/api/node-schema-fetch', { type: this.editingNodeType })
                    .then(data => {
                        if (!data || !data.schema || !data.schema.properties) return;
                        this.editingNodeSchema = data.schema;
                        const built = this.buildSchemaFields(data.schema, node.data || {});
                        this.editingNodeSchemaFields = built.fields;
                        this.editingNodeForm = built.form;
                        this.initCodeEditors();
                    })
                    .catch(() => { /* keep JSON fallback */ });
            },

            /**
             * Persist edited node properties back into the Drawflow node
             */
            saveNodeProperties() {
                let data;
                if (this.editingNodeSchema) {
                    // Flush live CodeMirror buffers into the form before
                    // collecting — the change listener already syncs,
                    // but a read here makes the round-trip independent
                    // of event timing.
                    Object.keys(this.codeEditorInst).forEach(k => {
                        this.editingNodeForm[k] = this.codeEditorInst[k].getValue();
                    });
                    const errors = {};
                    data = {};
                    // Preserve non-schema keys already on the node
                    const node = this.editor.getNodeFromId(this.editingNodeId);
                    Object.keys((node && node.data) || {}).forEach(key => {
                        if (!this.editingNodeForm.hasOwnProperty(key)) data[key] = node.data[key];
                    });
                    for (const f of this.editingNodeSchemaFields) {
                        let v = this.editingNodeForm[f.key];
                        if (f.field === 'json') {
                            if (v === '' || v === undefined) { v = {}; }
                            else {
                                try { v = JSON.parse(v); }
                                catch (err) { errors[f.key] = 'Invalid JSON: ' + err.message; continue; }
                            }
                        } else if (f.field === 'number') {
                            v = (v === '' || v === undefined || v === null) ? '' : Number(v);
                        }
                        if (f.required && (v === '' || v === undefined || v === null)) {
                            errors[f.key] = f.title + ' is required';
                            continue;
                        }
                        data[f.key] = v;
                    }
                    this.editingNodeFormErrors = errors;
                    if (Object.keys(errors).length) return;
                } else {
                    try {
                        data = JSON.parse(this.editingNodeData || '{}');
                    } catch (err) {
                        this.editingNodeDataError = 'Invalid JSON: ' + err.message;
                        return;
                    }
                }
                data.name = this.editingNodeName;
                data.type = this.editingNodeType;
                // Node-RED "d": true disables the node; the flag is
                // removed entirely when re-enabled.
                if (this.editingNodeDisabled) {
                    data.d = true;
                } else {
                    delete data.d;
                    delete data.disabled;
                }
                if (this.editingNodeType === 'switch') {
                    data.outputs = this.switchOutputLabels(data);
                }
                if (this.editingNodeType === 'checkpoint') {
                    data.outputs = this.checkpointRouteLabels(data);
                }
                const errorCatchType = this.supportsErrorCatch(this.editingNodeType)
                    ? (this.findType(this.editingNodeType) || { id: this.editingNodeType, outputs: 1 })
                    : null;
                if (errorCatchType) {
                    data.outputs = this.errorCatchOutputLabels(errorCatchType, data);
                }
                this.editor.updateNodeDataFromId(this.editingNodeId, data);
                // Resize output ports for config-driven types (switch,
                // checkpoint, errorCatch) — no-op for fixed-port types.
                this.syncDynamicOutputs(this.editingNodeId);
                this.refreshNodeDom(this.editingNodeId, data);
                this.refreshDisabledDom();
                // updateNodeDataFromId does NOT emit nodeDataChanged
                // (that event only comes from inline contenteditable
                // edits) — mark dirty + history here or the property
                // change would deploy stale config.
                this.dirty = true;
                this.pushHistory();
                bootstrap.Modal.getInstance(this.$refs.nodePropsModal).hide();
            },

            /**
             * Open the New Workflow modal
             */
            createWorkflow() {
                this.newWorkflowName = '';
                this.createError = '';
                bootstrap.Modal.getOrCreateInstance(this.$refs.newWorkflowModal).show();
            },

            /**
             * Create the workflow via the API and open it in the editor
             */
            async confirmCreateWorkflow() {
                const name = this.newWorkflowName.trim();
                if (!name) {
                    this.createError = 'Workflow name is required';
                    return;
                }
                try {
                    const data = await this.api('/api/workflow-create', { name, nodes: [], edges: [] });
                    this.workflows.push(data.workflow);
                    bootstrap.Modal.getInstance(this.$refs.newWorkflowModal).hide();
                    this.selectWorkflow(data.workflow);
                } catch (err) {
                    this.createError = err.message;
                }
            },

            /**
             * Show transient save feedback in the toolbar
             */
            setSaveStatus(status, message) {
                this.saveStatus = status;
                this.saveStatusMessage = message || '';
                if (status === 'saved') {
                    setTimeout(() => {
                        if (this.saveStatus === 'saved') this.saveStatus = '';
                    }, 2500);
                }
            },

            /**
             * Save the current flow to the API
             */
            async saveWorkflow() {
                if (!this.selectedWorkflow) return;
                const flow = this.exportFlow();
                // Component definitions are canvas childFlows now: each
                // role:"component" child flow serializes back into a
                // type:"component" definition node (component_child_flow_tree).
                this.serializeComponentDefs().forEach(d => flow.nodes.push(d));
                this.setSaveStatus('saving', 'Saving...');
                try {
                    // Preserve unrelated view keys (inboxCapacity,
                    // inboxDurable, ...) and merge the env table over
                    // the canvas-produced view (zoom, groups).
                    const view = Object.assign(
                        {},
                        (this.selectedWorkflow && this.selectedWorkflow.view) || {},
                        this.currentView()
                    );
                    if (this.flowEnv.length) {
                        view.env = this.flowEnv;
                    } else {
                        delete view.env;
                    }
                    const data = await this.api('/api/workflow-update', {
                        id: this.selectedWorkflow.id,
                        name: this.selectedWorkflow.name,
                        description: this.selectedWorkflow.description || '',
                        nodes: flow.nodes,
                        edges: flow.edges,
                        view: view
                    });
                    this.selectedWorkflow = data.workflow;
                    this.dirty = false;
                    this.setSaveStatus('saved', 'Saved');
                    return true;
                } catch (err) {
                    this.setSaveStatus('error', 'Save failed: ' + err.message);
                    return false;
                }
            },

            /* ---- Workflow properties (rename + flow-level view settings) ---- */

            /**
             * Queue nodes eligible as the designated problem queue
             * (view.onError): every class:"queue" node across the flow
             * childFlows. Checkpoint nodes are intentionally excluded —
             * problemQueueIDFromView rejects them (an approval gate is
             * not a dead-letter queue). Canvas ids ARE the persisted
             * backend node ids (exportFlow writes them through).
             */
            queueNodeOptions() {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const out = [];
                Object.keys(df).forEach(flowName => {
                    const meta = (typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(flowName) || {}) : {};
                    if ((meta.role || 'flow') === 'component') return;
                    const nodes = df[flowName].data || {};
                    Object.keys(nodes).forEach(id => {
                        if (nodes[id].class !== 'queue') return;
                        const label = (nodes[id].data && nodes[id].data.name) || String(id);
                        out.push({ id: String(id), label: label + ' — ' + (meta.label || flowName) });
                    });
                });
                return out;
            },

            /**
             * Open the workflow properties modal for the selected
             * workflow. Reads name/description off the document and the
             * flow-level settings off selectedWorkflow.view; writes are
             * uncommitted until saveWorkflowProps.
             */
            openWorkflowProps() {
                if (!this.selectedWorkflow) return;
                const view = this.selectedWorkflow.view || {};
                this.wfProps = {
                    name: this.selectedWorkflow.name || '',
                    description: this.selectedWorkflow.description || '',
                    onError: typeof view.onError === 'string' ? view.onError : '',
                    maxSteps: view.maxSteps !== undefined && view.maxSteps !== null ? String(view.maxSteps) : '',
                    inboxCapacity: view.inboxCapacity !== undefined && view.inboxCapacity !== null ? String(view.inboxCapacity) : '',
                    inboxPolicy: typeof view.inboxPolicy === 'string' ? view.inboxPolicy : '',
                    // Tri-state: '' keeps the key unset (engine default)
                    inboxDurable: view.inboxDurable === undefined || view.inboxDurable === null
                        ? '' : (view.inboxDurable === true || view.inboxDurable === 'true' ? 'true' : 'false')
                };
                // Keep a resolvable option when the stored onError id
                // points at a queue node deleted since (stale value
                // stays editable instead of silently dropping).
                this.wfQueueOptions = this.queueNodeOptions();
                const cur = this.wfProps.onError;
                if (cur && cur !== 'auto' && !this.wfQueueOptions.some(o => o.id === cur)) {
                    this.wfQueueOptions.push({ id: cur, label: cur + ' (missing node)' });
                }
                this.wfPropsError = '';
                bootstrap.Modal.getOrCreateInstance(this.$refs.workflowPropsModal).show();
            },

            /**
             * Sidebar affordance: selecting another workflow first so
             * the properties modal always edits the loaded document
             * (the canvas view keys must match the canvas about to be
             * saved).
             */
            async openWorkflowPropsFor(workflow) {
                if (!workflow) return;
                if (!this.selectedWorkflow || this.selectedWorkflow.id !== workflow.id) {
                    await this.selectWorkflow(workflow);
                }
                this.openWorkflowProps();
            },

            /**
             * Sidebar affordance: the "+" on an unselected workflow row
             * adds a component definition to that workflow — it selects
             * the workflow first so createComponent edits the loaded
             * document (same pattern as openWorkflowPropsFor).
             */
            async addComponentToWorkflow(workflow) {
                if (!workflow) return;
                if (!this.selectedWorkflow || this.selectedWorkflow.id !== workflow.id) {
                    await this.selectWorkflow(workflow);
                }
                this.createComponent();
            },

            /**
             * Commit the modal: name/description land on the workflow
             * document (sent by saveWorkflow), the view fields merge
             * into selectedWorkflow.view — set keys overwrite, empty
             * fields DELETE the key so the engine default applies, and
             * unrelated keys (openFlows, env, disabledFlows,
             * groups, ...) are untouched.
             */
            saveWorkflowProps() {
                if (!this.selectedWorkflow) return;
                const name = (this.wfProps.name || '').trim();
                if (!name) {
                    this.wfPropsError = 'Workflow name is required';
                    return;
                }
                // Validate numbers before mutating anything
                let maxSteps = null;
                if (this.wfProps.maxSteps !== '' && this.wfProps.maxSteps !== null) {
                    maxSteps = Number(this.wfProps.maxSteps);
                    if (!isFinite(maxSteps)) {
                        this.wfPropsError = 'Max steps must be a number';
                        return;
                    }
                }
                let inboxCapacity = null;
                if (this.wfProps.inboxCapacity !== '' && this.wfProps.inboxCapacity !== null) {
                    inboxCapacity = Number(this.wfProps.inboxCapacity);
                    if (!isFinite(inboxCapacity)) {
                        this.wfPropsError = 'Inbox capacity must be a number';
                        return;
                    }
                }

                this.selectedWorkflow.name = name;
                this.selectedWorkflow.description = (this.wfProps.description || '').trim();
                if (!this.selectedWorkflow.view || typeof this.selectedWorkflow.view !== 'object') {
                    this.selectedWorkflow.view = {};
                }
                const view = this.selectedWorkflow.view;
                const onError = (this.wfProps.onError || '').trim();
                if (onError) view.onError = onError; else delete view.onError;
                if (maxSteps !== null) view.maxSteps = maxSteps; else delete view.maxSteps;
                if (inboxCapacity !== null) view.inboxCapacity = inboxCapacity; else delete view.inboxCapacity;
                if (this.wfProps.inboxPolicy) view.inboxPolicy = this.wfProps.inboxPolicy;
                else delete view.inboxPolicy;
                if (this.wfProps.inboxDurable === '') delete view.inboxDurable;
                else view.inboxDurable = this.wfProps.inboxDurable === 'true';

                // The sidebar list row shows the name without a refetch
                const row = this.workflows.find(w => w.id === this.selectedWorkflow.id);
                if (row) {
                    row.name = name;
                    row.description = this.selectedWorkflow.description;
                }
                this.dirty = true;
                this.pushHistory();
                bootstrap.Modal.getInstance(this.$refs.workflowPropsModal).hide();
                this.showToast('Workflow properties updated — press Save to persist', 'info');
            },

            /* ---- Execution controls (run/stop + status polling) ---- */

            /**
             * Start the selected workflow and begin polling for status
             */
            async runWorkflow() {
                if (!this.selectedWorkflow || this.running) return;
                if (this.dirty) {
                    this.showToast('Unsaved changes — saving before deploy', 'info');
                    if (!(await this.saveWorkflow())) {
                        this.showToast('Deploy aborted — could not save changes', 'danger');
                        return;
                    }
                }
                this.clearNodeStatuses();
                this.lastEventSeq = 0;
                try {
                    const data = await this.api('/api/workflow-start', { id: this.selectedWorkflow.id });
                    this.executionId = data.execution_id || null;
                    this.running = true;
                    this.debugOpen = true;
                    this.setSaveStatus('saved', 'Workflow deployed');
                    this.startPolling();
                } catch (err) {
                    this.showToast('Start failed: ' + err.message, 'danger');
                }
            },

            /**
             * Stop the running workflow
             */
            async stopWorkflow() {
                if (!this.selectedWorkflow) return;
                try {
                    await this.api('/api/workflow-stop', { id: this.selectedWorkflow.id });
                    this.showToast('Workflow undeployed', 'info');
                } catch (err) {
                    this.showToast('Stop failed: ' + err.message, 'danger');
                }
                this.running = false;
                this.runnerId = null;
                this.stopPolling();
                this.clearNodeStatuses();
            },

            /**
             * Manually fire an inject node (editor bolt button).
             * The backend expects the persisted node id; Drawflow canvas
             * ids match for nodes added this session but are reassigned
             * on reload — drawflowToBackend bridges the two.
             */
            async fireInject(nodeId) {
                if (!this.selectedWorkflow) return;
                if (!this.running) {
                    this.showToast('Deploy the workflow before firing an inject', 'info');
                    return;
                }
                const backendId = this.drawflowToBackend[String(nodeId)] || String(nodeId);
                try {
                    await this.api('/api/node-inject', {
                        workflow_id: this.selectedWorkflow.id,
                        node_id: backendId
                    });
                    this.showToast('Inject fired', 'success');
                } catch (err) {
                    this.showToast('Inject failed: ' + err.message, 'danger');
                }
            },

            /**
             * Redeploy: undeploy then deploy again in one click —
             * restarts listeners and picks up saved changes.
             */
            async redeployWorkflow() {
                if (!this.selectedWorkflow) return;
                await this.stopWorkflow();
                await this.runWorkflow();
            },

            /**
             * Poll /api/workflow-status every 1.5s while running
             */
            startPolling() {
                this.stopPolling();
                this.pollStatus();
                this.pollTimer = setInterval(() => this.pollStatus(), 1500);
            },

            stopPolling() {
                if (this.pollTimer) {
                    clearInterval(this.pollTimer);
                    this.pollTimer = null;
                }
            },

            /**
             * Fetch one status snapshot: updates node dots + appends
             * new events to the debug panel. Stops polling once the
             * engine reports the workflow is no longer running.
             */
            async pollStatus() {
                if (!this.selectedWorkflow) { this.stopPolling(); return; }
                try {
                    const data = await this.api('/api/workflow-status', {
                        id: this.selectedWorkflow.id,
                        since: this.lastEventSeq
                    });
                    this.applyNodeStatuses(data.nodes || {});
                    this.runnerId = data.runner_id || null;
                    this.pollQueueDepths();
                    this.pollPendingCounts();
                    if (Array.isArray(data.events)) this.appendDebugEvents(data.events);
                    if (typeof data.last_seq === 'number') this.lastEventSeq = data.last_seq;
                    if (data.status !== 'running') {
                        this.running = false;
                        this.runnerId = null;
                        this.stopPolling();
                    }
                } catch (err) {
                    // Transient poll failures should not kill the run UI
                    console.warn('Status poll failed:', err.message);
                }
            },

            /**
             * Draw/update a Node-RED style status dot under each node
             * (statuses: running | ok | error, keyed by node id)
             */
            applyNodeStatuses(statuses) {
                Object.keys(statuses).forEach(nodeId => {
                    const el = document.getElementById('node-' + (this.backendToDrawflow[nodeId] || nodeId));
                    if (!el) return;
                    const body = el.querySelector('.node-body');
                    if (!body) return;
                    let dot = body.querySelector('.node-status-dot');
                    if (!dot) {
                        dot = document.createElement('div');
                        dot.className = 'node-status-dot';
                        body.appendChild(dot);
                    }
                    const status = statuses[nodeId];
                    dot.className = 'node-status-dot st-' + status;
                    dot.textContent = status;
                });
            },

            /**
             * Remove all status dots from the canvas
             */
            clearNodeStatuses() {
                document.querySelectorAll('#drawflow .node-status-dot')
                    .forEach(dot => dot.remove());
                document.querySelectorAll('#drawflow .queue-depth')
                    .forEach(badge => {
                        badge.textContent = '0';
                        badge.classList.remove('text-bg-warning');
                        badge.classList.add('text-bg-secondary');
                    });
                document.querySelectorAll('#drawflow .pending-badge')
                    .forEach(badge => badge.remove());
            },

            /* ---- Per-node pending-count badges (inbox backlog) ---- */

            /**
             * Fetch {nodeId: count} from GET /api/workflows/{id}/pending
             * and refresh every node's corner badge. Runs on the status
             * poll cycle, so it only fires while a workflow is running;
             * failures leave the previous badges in place.
             */
            async pollPendingCounts() {
                if (!this.selectedWorkflow || !this.running) return;
                try {
                    const counts = await this.apiGet(
                        '/api/workflows/' + encodeURIComponent(this.selectedWorkflow.id) + '/pending');
                    this.applyPendingCounts(counts || {});
                } catch (err) {
                    console.warn('Pending-count poll failed:', err.message);
                }
            },

            /**
             * Draw/update the corner pill on every canvas node: visible
             * when the node has queued inbox rows, hidden at 0. Nodes
             * absent from the counts map get a hidden badge.
             */
            applyPendingCounts(counts) {
                document.querySelectorAll('#drawflow .drawflow-node').forEach(el => {
                    const dfId = String(el.id || '').replace(/^node-/, '');
                    const backendId = this.drawflowToBackend[dfId] || dfId;
                    const count = counts[backendId] || 0;
                    let badge = el.querySelector('.pending-badge');
                    if (!badge) {
                        badge = document.createElement('span');
                        badge.className = 'pending-badge badge text-bg-info d-none';
                        el.appendChild(badge);
                    }
                    badge.textContent = String(count);
                    badge.title = count + ' pending message' + (count === 1 ? '' : 's');
                    badge.classList.toggle('d-none', count <= 0);
                });
            },

            /* ---- Queue nodes: depth badges + inspection panel ---- */

            /**
             * Refresh the depth badge of every queue node in the current
             * flow by querying the inspection API. Runs on the status poll
             * cycle; failures are ignored (queue may not be deployed).
             */
            async pollQueueDepths() {
                if (!this.selectedWorkflow) return;
                const queues = (this.selectedWorkflow.nodes || [])
                    .filter(n => n.type === 'queue' || n.type === 'checkpoint');
                for (const node of queues) {
                    try {
                        const data = await this.api('/api/queue-items', {
                            workflow_id: this.selectedWorkflow.id,
                            node_id: node.id
                        });
                        this.updateQueueBadge(node.id, data);
                    } catch (err) {
                        // Queue not live (undeployed): leave badge at 0
                    }
                }
            },

            /**
             * Update one queue node's depth badge from an /api/queue-items
             * response ({count, hold, items}).
             */
            updateQueueBadge(backendId, info) {
                const el = document.getElementById('node-' + (this.backendToDrawflow[backendId] || backendId));
                if (!el) return;
                const badge = el.querySelector('.queue-depth');
                if (!badge) return;
                badge.textContent = String(info.count || 0);
                badge.classList.toggle('text-bg-warning', !!info.hold);
                badge.classList.toggle('text-bg-secondary', !info.hold);
                badge.title = (info.count || 0) + ' queued' + (info.hold ? ' (held)' : '');
            },

            /**
             * Open the queue inspection modal for a canvas node.
             */
            openQueueInspect(dfNodeId) {
                const backendId = this.drawflowToBackend[String(dfNodeId)] || String(dfNodeId);
                this.queueInspect.nodeId = backendId;
                this.queueInspect.error = '';
                this.refreshQueueInspect();
                bootstrap.Modal.getOrCreateInstance(this.$refs.queueModal).show();
            },

            /**
             * Fetch the queue's queued items into the modal state.
             */
            async refreshQueueInspect() {
                if (!this.selectedWorkflow || !this.queueInspect.nodeId) return;
                this.queueInspect.loading = true;
                try {
                    const data = await this.api('/api/queue-items', {
                        workflow_id: this.selectedWorkflow.id,
                        node_id: this.queueInspect.nodeId
                    });
                    this.queueInspect.items = data.items || [];
                    this.queueInspect.count = data.count || 0;
                    this.queueInspect.hold = !!data.hold;
                    this.queueInspect.routes = data.routes || [];
                    this.queueInspect.error = '';
                    this.updateQueueBadge(this.queueInspect.nodeId, data);
                } catch (err) {
                    this.queueInspect.error = err.message;
                } finally {
                    this.queueInspect.loading = false;
                }
            },

            /**
             * Toggle the queue's hold gate via the API.
             */
            async queueToggleHold() {
                if (!this.queueInspect.nodeId) return;
                try {
                    await this.api('/api/queue-hold', {
                        workflow_id: this.selectedWorkflow.id,
                        node_id: this.queueInspect.nodeId,
                        hold: !this.queueInspect.hold
                    });
                    await this.refreshQueueInspect();
                } catch (err) {
                    this.queueInspect.error = err.message;
                }
            },

            /**
             * Delete one queued item.
             */
            async queueDeleteItem(seq) {
                try {
                    await this.api('/api/queue-item-delete', {
                        workflow_id: this.selectedWorkflow.id,
                        node_id: this.queueInspect.nodeId,
                        seq: seq
                    });
                    await this.refreshQueueInspect();
                } catch (err) {
                    this.queueInspect.error = err.message;
                }
            },

            /**
             * Requeue one item: release it downstream immediately. For
             * checkpoints pass the clicked route label — the item is
             * delivered only on that edge.
             */
            async queueRequeueItem(seq, route) {
                try {
                    await this.api('/api/queue-item-requeue', {
                        workflow_id: this.selectedWorkflow.id,
                        node_id: this.queueInspect.nodeId,
                        seq: seq,
                        route: route || ''
                    });
                    await this.refreshQueueInspect();
                } catch (err) {
                    this.queueInspect.error = err.message;
                }
            },

            /* ---- Run details: per-node execution journal ---- */

            /**
             * Open the run-details modal for a canvas node (context
             * menu entry; dfNodeId is the Drawflow id, resolved to the
             * backend node id like openQueueInspect).
             */
            openRunDetails(dfNodeId) {
                const backendId = this.drawflowToBackend[String(dfNodeId)] || String(dfNodeId);
                this.runDetails.nodeId = backendId;
                this.runDetails.error = '';
                this.runDetails.entries = [];
                this.refreshRunDetails();
                bootstrap.Modal.getOrCreateInstance(this.$refs.runDetailsModal).show();
            },

            /**
             * Fetch the node's execution journal into the modal state.
             * The API returns entries oldest-first; the panel lists the
             * most recent run at the top.
             */
            async refreshRunDetails() {
                if (!this.selectedWorkflow || !this.runDetails.nodeId) return;
                this.runDetails.loading = true;
                try {
                    const data = await this.api('/api/node-run-journal', {
                        workflow_id: this.selectedWorkflow.id,
                        node_id: this.runDetails.nodeId
                    });
                    this.runDetails.entries = (data.entries || []).slice().reverse();
                    this.runDetails.size = data.size || 0;
                    this.runDetails.error = '';
                } catch (err) {
                    this.runDetails.error = err.message;
                } finally {
                    this.runDetails.loading = false;
                }
            },

            /**
             * Pretty-print a journaled payload summary (already
             * credential-masked and size-bounded server-side).
             */
            runEntryJSON(value) {
                if (value === null || value === undefined) return 'null';
                try {
                    return JSON.stringify(value, null, 2);
                } catch (err) {
                    return String(value);
                }
            },

            /**
             * Format a journal timestamp as HH:MM:SS.mmm local time.
             */
            runEntryTime(iso) {
                if (!iso) return '';
                const d = new Date(iso);
                if (isNaN(d)) return iso;
                return d.toLocaleTimeString() + '.' + String(d.getMilliseconds()).padStart(3, '0');
            },

            /**
             * Format a queue age in seconds as a short human string.
             */
            queueItemAge(seconds) {
                if (typeof seconds !== 'number') return '';
                if (seconds < 60) return Math.round(seconds) + 's';
                if (seconds < 3600) return Math.round(seconds / 60) + 'm';
                return Math.round(seconds / 3600) + 'h';
            },

            /* ---- Debug/log panel ---- */

            toggleDebug() {
                this.debugOpen = !this.debugOpen;
            },

            /**
             * Append events to the panel and keep it scrolled to the bottom
             */
            appendDebugEvents(events) {
                events.forEach(ev => this.debugEvents.push(ev));
                // Keep the buffer bounded client-side too
                if (this.debugEvents.length > 500) {
                    this.debugEvents.splice(0, this.debugEvents.length - 500);
                }
                this.$nextTick(() => {
                    const body = this.$refs.debugBody;
                    if (body) body.scrollTop = body.scrollHeight;
                });
            },

            clearDebug() {
                this.debugEvents = [];
            },

            /**
             * Whether an event message looks like JSON (object/array);
             * such values render in a <pre> block so pretty-printed
             * debug node output keeps its indentation
             */
            isJSONMessage(msg) {
                if (typeof msg !== 'string') return false;
                const t = msg.trim();
                return t.startsWith('{') || t.startsWith('[');
            },

            /**
             * Format an RFC3339 event timestamp as HH:MM:SS for the panel
             */
            formatEventTime(t) {
                const d = new Date(t);
                if (isNaN(d)) return '';
                return d.toLocaleTimeString();
            },

            /* ---- Canvas controls (task 08) ---- */

            zoomIn() { this.editor.zoom_in(); this.scheduleMinimap(); },
            zoomOut() { this.editor.zoom_out(); this.scheduleMinimap(); },
            zoomReset() { this.editor.zoom_reset(); this.scheduleMinimap(); },

            /**
             * Bounding box of all nodes in the current child flow
             * (approximate node size 200x90)
             */
            nodeBounds() {
                const flow = this.editor.drawflow.drawflow[this.editor.module];
                const data = (flow && flow.data) || {};
                const NODE_W = NODE_SIZE.w, NODE_H = NODE_SIZE.h;
                let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity, count = 0;
                Object.keys(data).forEach(id => {
                    const n = data[id];
                    count++;
                    minX = Math.min(minX, n.pos_x);
                    minY = Math.min(minY, n.pos_y);
                    maxX = Math.max(maxX, n.pos_x + NODE_W);
                    maxY = Math.max(maxY, n.pos_y + NODE_H);
                });
                if (!count) return null;
                return { minX, minY, maxX, maxY };
            },

            /**
             * Zoom + pan so all nodes in the current child flow fit the viewport
             * (floweditor zoomFit: measures the real DOM bounds, ports and
             * wires included, and dispatches 'zoom')
             */
            fitToContent() {
                const fitted = this.editor.zoomFit({ padding: 40, maxZoom: this.editor.zoom_max });
                if (fitted === null) {
                    this.showToast('No nodes to fit', 'info');
                    return;
                }
                this.dirty = true;
                this.scheduleMinimap();
            },

            /**
             * Pan so a node sits in the middle of the viewport
             * (floweditor centerNode; node context menu "Center")
             */
            centerOnNode(nodeId) {
                this.editor.centerNode(nodeId);
                this.scheduleMinimap();
            },

            /**
             * Debounced minimap refresh (node moves/zooms fire rapidly)
             */
            scheduleMinimap() {
                if (this.minimapTimer) clearTimeout(this.minimapTimer);
                this.minimapTimer = setTimeout(() => this.updateMinimap(), 250);
            },

            /**
             * Recompute the minimap: one small rect per node plus the
             * current viewport rectangle, all scaled into ~160x110 px.
             */
            updateMinimap() {
                const flow = this.editor.drawflow.drawflow[this.editor.module];
                const data = (flow && flow.data) || {};
                const b = this.nodeBounds();
                const MW = 160, MH = 110, pad = 8;
                const nodes = [];
                let view = null;
                if (b) {
                    const bw = Math.max(b.maxX - b.minX, 1);
                    const bh = Math.max(b.maxY - b.minY, 1);
                    const scale = Math.min((MW - 2 * pad) / bw, (MH - 2 * pad) / bh);
                    const offX = pad + ((MW - 2 * pad) - bw * scale) / 2;
                    const offY = pad + ((MH - 2 * pad) - bh * scale) / 2;
                    Object.keys(data).forEach(id => {
                        const n = data[id];
                        const type = this.findType(n.class);
                        nodes.push({
                            x: offX + (n.pos_x - b.minX) * scale,
                            y: offY + (n.pos_y - b.minY) * scale,
                            w: Math.max(4, 200 * scale),
                            h: Math.max(3, 90 * scale),
                            color: (type && type.color) || '#adb5bd'
                        });
                    });
                    const container = this.editor.precanvas;
                    if (container) {
                        const z = this.editor.zoom || 1;
                        const vx = (-this.editor.canvas_x) / z;
                        const vy = (-this.editor.canvas_y) / z;
                        view = {
                            x: offX + (vx - b.minX) * scale,
                            y: offY + (vy - b.minY) * scale,
                            w: (container.clientWidth / z) * scale,
                            h: (container.clientHeight / z) * scale
                        };
                    }
                }
                this.minimapNodes = nodes;
                this.minimapView = view;
            },

            /* ---- Undo/redo history (task 09) ---- */

            /**
             * Serialized snapshot of the whole editor (all childFlows);
             * editor.export() carries groups natively since the model
             * moved into floweditor (task 39).
             */
            snapshotNow() {
                try {
                    return JSON.stringify(this.editor.export());
                } catch (err) {
                    return null;
                }
            },

            /**
             * Push the pre-change snapshot onto the undo stack and record
             * the current state as the new baseline. Call after mutations.
             */
            pushHistory() {
                if (this.suppressHistory) return;
                const prev = this.historySnapshot !== null ? this.historySnapshot : this.snapshotNow();
                if (prev === null) return;
                this.undoStack.push(prev);
                if (this.undoStack.length > 50) this.undoStack.shift();
                this.redoStack = [];
                this.historySnapshot = this.snapshotNow();
            },

            /**
             * nodeMoved can fire repeatedly during a drag; collapse bursts
             * into a single history entry.
             */
            pushHistoryDebounced() {
                if (this.suppressHistory) return;
                if (this.historyDebounce) {
                    clearTimeout(this.historyDebounce);
                }
                this.historyDebounce = setTimeout(() => {
                    this.historyDebounce = null;
                    this.pushHistory();
                }, 400);
            },

            undo() {
                if (!this.undoStack.length) {
                    this.showToast('Nothing to undo', 'info');
                    return;
                }
                const current = this.snapshotNow();
                const prev = this.undoStack.pop();
                if (current) this.redoStack.push(current);
                this.applySnapshot(prev);
            },

            redo() {
                if (!this.redoStack.length) {
                    this.showToast('Nothing to redo', 'info');
                    return;
                }
                const current = this.snapshotNow();
                const next = this.redoStack.pop();
                if (current) this.undoStack.push(current);
                this.applySnapshot(next);
            },

            /**
             * Restore a serialized snapshot into the editor
             */
            applySnapshot(snap) {
                if (!snap) return;
                this.suppressHistory = true;
                try {
                    const parsed = JSON.parse(snap);
                    this.editor.import(parsed.canvas || parsed);
                } catch (err) {
                    console.warn('Snapshot restore failed:', err);
                }
                this.suppressHistory = false;
                this.historySnapshot = snap;
                this.syncTabsFromEditor();
                this.refreshAllOutputLabels();
                this.scheduleMinimap();
                this.dirty = true;
            },

            /**
             * Reset history (on workflow switch / initial render)
             */
            clearHistory() {
                this.undoStack = [];
                this.redoStack = [];
                if (this.historyDebounce) {
                    clearTimeout(this.historyDebounce);
                    this.historyDebounce = null;
                }
                this.historySnapshot = this.snapshotNow();
            },

            /* ---- Clipboard: copy/paste/duplicate (task 09) ---- */

            /**
             * Global keydown handler for editor shortcuts. Ignores events
             * originating in form fields or open modals.
             */
            onGlobalKeydown(e) {
                const t = e.target;
                const tag = (t.tagName || '').toLowerCase();
                if (tag === 'input' || tag === 'textarea' || tag === 'select' || t.isContentEditable) return;
                if (t.closest && t.closest('.modal')) return;
                const key = (e.key || '').toLowerCase();
                // With editor.multiselect the canvas keydown already removed
                // the selection/selected node; the element check keeps us
                // from deleting it a second time
                if ((key === 'delete' || key === 'backspace') && this.lastSelectedNodeId
                    && document.getElementById('node-' + this.lastSelectedNodeId)) {
                    e.preventDefault();
                    this.deleteNode(this.lastSelectedNodeId);
                    return;
                }
                if (!(e.ctrlKey || e.metaKey)) return;
                if (key === 'z' && !e.shiftKey) {
                    e.preventDefault();
                    this.undo();
                } else if (key === 'y' || (key === 'z' && e.shiftKey)) {
                    e.preventDefault();
                    this.redo();
                } else if (key === 'c') {
                    if (this.lastSelectedNodeId) {
                        e.preventDefault();
                        this.copySelectedNode();
                    }
                } else if (key === 'v') {
                    if (this.clipboardNode) {
                        e.preventDefault();
                        this.pasteNode();
                    }
                } else if (key === 'd') {
                    if (this.lastSelectedNodeId) {
                        e.preventDefault();
                        this.duplicateNode(this.lastSelectedNodeId);
                    }
                }
            },

            /**
             * Copy the selected node's definition to the internal clipboard
             */
            copySelectedNode() {
                const node = this.editor.getNodeFromId(this.lastSelectedNodeId);
                if (!node) {
                    this.showToast('Select a node to copy', 'info');
                    return;
                }
                this.clipboardNode = {
                    name: node.name,
                    class: node.class,
                    html: node.html,
                    data: JSON.parse(JSON.stringify(node.data || {})),
                    inputs: Object.keys(node.inputs || {}).length,
                    outputs: Object.keys(node.outputs || {}).length
                };
                this.clipboardPos = { x: node.pos_x, y: node.pos_y };
                this.showToast('Copied "' + (node.data.name || node.name) + '"', 'info');
            },

            /**
             * Paste a clone of the clipboard node, offset +30px from the
             * last paste position (cascading pastes)
             */
            pasteNode() {
                const c = this.clipboardNode;
                if (!c) {
                    this.showToast('Nothing to paste', 'info');
                    return;
                }
                this.clipboardPos.x += 30;
                this.clipboardPos.y += 30;
                const data = JSON.parse(JSON.stringify(c.data));
                const id = this.editor.addNode(
                    c.name, c.inputs, c.outputs,
                    this.clipboardPos.x, this.clipboardPos.y,
                    c.class, data, c.html, false);
                this.refreshOutputLabels(id);
                this.dirty = true;
            },

            /**
             * Clone an existing node offset by +30px (context menu / Ctrl+D)
             */
            duplicateNode(nodeId) {
                const node = this.editor.getNodeFromId(nodeId);
                if (!node) return;
                const data = JSON.parse(JSON.stringify(node.data || {}));
                const id = this.editor.addNode(
                    node.name,
                    Object.keys(node.inputs || {}).length,
                    Object.keys(node.outputs || {}).length,
                    node.pos_x + 30, node.pos_y + 30,
                    node.class, data, node.html, false);
                this.refreshOutputLabels(id);
                this.dirty = true;
            },

            /**
             * Delete a node by id (context menu); nodeRemoved records history
             */
            deleteNode(nodeId) {
                try {
                    this.editor.removeNodeId('node-' + nodeId);
                } catch (err) {
                    console.warn('Delete failed:', err);
                }
                this.dirty = true;
            },

            /**
             * Right-click on a node: open the small context menu
             */
            onCanvasContextMenu(e) {
                const nodeEl = e.target.closest('.drawflow-node');
                if (!nodeEl) return;
                e.preventDefault();
                this.contextMenu = {
                    show: true,
                    x: e.clientX,
                    y: e.clientY,
                    nodeId: nodeEl.id.replace('node-', '')
                };
            },

            closeContextMenu() {
                this.contextMenu.show = false;
            },

            /* ---- Flow tabs + child flow navigator (task 11 / child_flows_first_class) ---- */

            /**
             * Switch the visible canvas to another child flow/tab
             */
            switchTab(tab) {
                if (this.editor.module !== tab.childFlow) {
                    this.editor.changeModule(tab.childFlow);
                }
                this.activeTab = tab.childFlow;
                this.scheduleMinimap();
            },

            /**
             * Create a new empty child flow and open it in a tab (used by
             * both the tab "+" and the tree's "New flow" button).
             */
            addFlowTab() {
                const df = this.editor.drawflow.drawflow || {};
                let n = Object.keys(df).length + 1;
                let name = 'Flow ' + n;
                while (df[name]) {
                    n++;
                    name = 'Flow ' + n;
                }
                this.editor.addModule(name);
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(name, { label: name, role: 'flow', enabled: true });
                }
                const tab = { childFlow: name, label: name, role: 'flow' };
                this.flowTabs.push(tab);
                this.knownFlows.push(name);
                this.flowTreeVersion++;
                this.switchTab(tab);
                this.pushHistory();
                this.dirty = true;
                return name;
            },

            /**
             * Close a tab — non-destructive. The child flow stays
             * materialized in Drawflow and listed in the tree; only the
             * tab goes away. The open set persists via view.openFlows,
             * so the close survives save + reload. Deleting a child flow is
             * a separate tree action (deleteFlowByName).
             */
            closeFlowTab(tab) {
                if (this.editor.module === tab.childFlow) {
                    const idx = this.flowTabs.findIndex(t => t.childFlow === tab.childFlow);
                    const next = this.flowTabs[idx === 0 ? 1 : idx - 1];
                    // Closing the last tab empties the workspace — the
                    // welcome screen takes over.
                    if (next && next !== tab) {
                        this.switchTab(next);
                    } else {
                        this.activeTab = null;
                    }
                }
                this.flowTabs = this.flowTabs.filter(t => t.childFlow !== tab.childFlow);
                this.flowTreeVersion++;
                this.dirty = true;
            },

            /* ---- Child flow navigator tree ---- */

            /**
             * Open a child flow from the tree: create its tab if absent,
             * then activate it. Already-open items just activate.
             */
            openFlowItem(item) {
                let tab = this.flowTabs.find(t => t.childFlow === item.childFlow);
                if (!tab) {
                    tab = { childFlow: item.childFlow, label: item.label, role: item.role };
                    this.flowTabs.push(tab);
                    // The open set persists via view.openFlows
                    this.dirty = true;
                    this.flowTreeVersion++;
                }
                this.switchTab(tab);
            },

            /**
             * Right-click on a tree item: open the child flow context menu.
             */
            onFlowContextMenu(e, item) {
                e.preventDefault();
                e.stopPropagation();
                this.flowContextMenu = {
                    show: true,
                    x: e.clientX,
                    y: e.clientY,
                    childFlow: item.childFlow
                };
            },

            closeFlowContextMenu() {
                this.flowContextMenu.show = false;
            },

            /* ---- Sidebar file tree (sidebar_file_tree) ---- */

            /**
             * Sanitized folder list persisted as view.treeGroups:
             * [{id,name,parentId,childFlows:[childFlowId]}]. Drops parent
             * ids pointing at deleted folders and child flow refs that no
             * longer resolve to a materialized child flow; a child flow id
             * claimed by two folders stays with the first.
             */
            serializeTreeGroups() {
                const ids = {};
                this.treeGroups.forEach(g => { ids[g.id] = true; });
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const claimed = {};
                return this.treeGroups.map(g => ({
                    id: g.id,
                    name: g.name,
                    parentId: (g.parentId && ids[g.parentId]) ? g.parentId : null,
                    childFlows: (g.childFlows || [])
                        .map(mid => this.resolveFlowName(String(mid)))
                        .filter(name => !!df[name])
                        .map(name => this.flowIdFor(name))
                        .filter(mid => {
                            if (claimed[mid]) return false;
                            claimed[mid] = true;
                            return true;
                        })
                }));
            },

            folderById(id) {
                return this.treeGroups.find(g => g.id === id) || null;
            },

            /** Display path "a/b/c" for a folder (context menu label). */
            folderPathFor(folder) {
                const parts = [folder.name];
                let cur = folder;
                let guard = 0;
                while (cur.parentId && guard++ < 32) {
                    cur = this.folderById(cur.parentId);
                    if (!cur) break;
                    parts.unshift(cur.name);
                }
                return parts.join(' / ');
            },

            /** Create a group folder under parentId (null = root). */
            createTreeFolder(parentId) {
                if (!this.selectedWorkflow) return;
                const name = (window.prompt('Folder name', 'New folder') || '').trim();
                if (!name) return;
                this.treeGroups.push({
                    id: uuidV7(),
                    name: name,
                    parentId: parentId || null,
                    childFlows: []
                });
                this.dirty = true;
            },

            renameTreeFolder(folder) {
                if (!folder) return;
                const name = (window.prompt('Rename folder', folder.name) || '').trim();
                if (!name || name === folder.name) return;
                folder.name = name;
                this.dirty = true;
            },

            /**
             * Delete a folder and its descendant folders. Organization
             * only — member childFlows are never touched and return to
             * the ungrouped root list.
             */
            deleteTreeFolder(folder) {
                if (!folder) return;
                if (!confirm(`Delete folder "${folder.name}"? Child flows inside move back to the root — nothing else is removed.`)) return;
                const doomed = {};
                const mark = id => {
                    doomed[id] = true;
                    this.treeGroups.filter(g => g.parentId === id).forEach(g => mark(g.id));
                };
                mark(folder.id);
                this.treeGroups = this.treeGroups.filter(g => !doomed[g.id]);
                this.dirty = true;
            },

            /**
             * Move a child flow into a folder (null = ungrouped root).
             * Membership is exclusive: the child flow id leaves every
             * other folder's list. Stored by child flow id so renames do
             * not break membership.
             */
            moveFlowToFolder(flowKey, folderId) {
                const mid = String(this.flowIdFor(flowKey));
                this.treeGroups.forEach(g => {
                    g.childFlows = (g.childFlows || []).filter(m => String(m) !== mid);
                });
                const folder = folderId ? this.folderById(folderId) : null;
                if (folder) {
                    if (!Array.isArray(folder.childFlows)) folder.childFlows = [];
                    folder.childFlows.push(mid);
                }
                this.dirty = true;
            },

            /** Create a child flow and place it inside a folder. */
            createFlowInFolder(role, folderId) {
                const key = role === 'component' ? this.createComponent() : this.addFlowTab();
                if (key) this.moveFlowToFolder(key, folderId);
            },

            toggleTreeNode(key) {
                this.collapsedTreeNodes[key] = !this.collapsedTreeNodes[key];
            },

            // Chevron on an unselected workflow selects it — expansion only
            // happens for the selected workflow, so a bare collapse-flag toggle
            // would be invisible there. Folders toggle regardless.
            onTreeChevronClick(row) {
                if (row.type === 'workflow' && !row.selected) {
                    this.collapsedTreeNodes[row.key] = false;
                    this.selectWorkflow(row.workflow);
                    return;
                }
                this.toggleTreeNode(row.key);
            },

            onTreeRowClick(row) {
                if (row.type === 'workflow') {
                    if (!row.selected) this.selectWorkflow(row.workflow);
                    return;
                }
                if (row.type === 'folder' || row.type === 'wgroup') {
                    this.toggleTreeNode(row.key);
                    return;
                }
                this.openFlowItem(row.item);
            },

            onTreeRowDblClick(row) {
                if (row.type === 'workflow') this.openWorkflowPropsFor(row.workflow);
            },

            onTreeRowContextMenu(e, row) {
                if (row.type === 'flow') {
                    this.onFlowContextMenu(e, row.item);
                    return;
                }
                if (row.type === 'folder') {
                    e.preventDefault();
                    e.stopPropagation();
                    this.folderContextMenu = {
                        show: true, x: e.clientX, y: e.clientY,
                        folderId: row.folder.id
                    };
                    return;
                }
                if (row.type === 'workflow') {
                    e.preventDefault();
                    e.stopPropagation();
                    this.workflowContextMenu = {
                        show: true, x: e.clientX, y: e.clientY,
                        workflowId: row.workflow.id
                    };
                    return;
                }
                if (row.type === 'wgroup') {
                    e.preventDefault();
                    e.stopPropagation();
                    this.workflowGroupContextMenu = {
                        show: true, x: e.clientX, y: e.clientY,
                        groupId: row.group.id
                    };
                }
            },

            closeFolderContextMenu() {
                this.folderContextMenu.show = false;
            },

            closeWorkflowContextMenu() {
                this.workflowContextMenu.show = false;
            },

            closeWorkflowGroupContextMenu() {
                this.workflowGroupContextMenu.show = false;
            },

            /**
             * Sidebar affordance: child flow-folder creation lives on the
             * workflow row's context menu — the folder list is per
             * workflow document, so select first like addComponentToWorkflow.
             */
            async createTreeFolderFor(workflow) {
                if (!workflow) return;
                if (!this.selectedWorkflow || this.selectedWorkflow.id !== workflow.id) {
                    await this.selectWorkflow(workflow);
                }
                this.createTreeFolder(null);
            },

            /* Child flow leaf drag-and-drop into folders (or back to the
               workflow root); workflow rows drag into workflow groups. */
            onTreeDragStart(e, row) {
                if (row.type === 'workflow') {
                    this.dragWorkflowId = row.workflow.id;
                    try {
                        e.dataTransfer.setData('text/x-flow-workflow', String(row.workflow.id));
                        e.dataTransfer.effectAllowed = 'move';
                    } catch (err) { /* drag data is best-effort */ }
                    return;
                }
                if (row.type !== 'flow') {
                    e.preventDefault();
                    return;
                }
                this.dragFlowKey = row.item.childFlow;
                try {
                    e.dataTransfer.setData('text/x-flow-childflow', row.item.childFlow);
                    e.dataTransfer.effectAllowed = 'move';
                } catch (err) { /* drag data is best-effort */ }
            },

            onTreeDragOver(e, row) {
                if (this.dragWorkflowId) {
                    const ok = row.type === 'wgroup' || row.type === 'workflow';
                    if (!ok) return;
                    e.preventDefault();
                    e.dataTransfer.dropEffect = 'move';
                    this.treeDropKey = row.key;
                    return;
                }
                if (!this.dragFlowKey) return;
                const ok = row.type === 'folder' ||
                    (row.type === 'workflow' && row.selected) ||
                    (row.type === 'flow');
                if (!ok) return;
                e.preventDefault();
                e.dataTransfer.dropEffect = 'move';
                this.treeDropKey = row.key;
            },

            onTreeDragLeave(row) {
                if (this.treeDropKey === row.key) this.treeDropKey = null;
            },

            onTreeDrop(e, row) {
                if (this.dragWorkflowId) {
                    const wid = this.dragWorkflowId;
                    this.dragWorkflowId = null;
                    this.treeDropKey = null;
                    e.preventDefault();
                    if (row.type === 'wgroup') {
                        this.moveWorkflowToGroup(wid, row.group.id);
                    } else if (row.type === 'workflow' && String(row.workflow.id) !== String(wid)) {
                        // Dropping onto a workflow files the dragged
                        // one next to it — that row's group or root.
                        const g = this.workflowGroupFor(row.workflow.id);
                        this.moveWorkflowToGroup(wid, g ? g.id : null);
                    }
                    return;
                }
                const key = this.dragFlowKey;
                this.dragFlowKey = null;
                this.treeDropKey = null;
                if (!key) return;
                e.preventDefault();
                if (row.type === 'folder') {
                    this.moveFlowToFolder(key, row.folder.id);
                } else if (row.type === 'workflow' && row.selected) {
                    this.moveFlowToFolder(key, null);
                } else if (row.type === 'flow') {
                    // Dropping onto a leaf files it next to that leaf:
                    // the leaf's folder (or root when ungrouped).
                    const folder = this.treeGroups.find(g =>
                        (g.childFlows || []).some(mid =>
                            this.resolveFlowName(String(mid)) === row.item.childFlow));
                    this.moveFlowToFolder(key, folder ? folder.id : null);
                }
            },

            /* ---- Child flow export/import (childflow_export_import) ---- */

            /**
             * Export one child flow as a portable child flow envelope
             * ({nodestore:"child_flow", version:1}) and copy it to the
             * clipboard. Saves first when the canvas is dirty so the
             * export reflects the visible state. External references
             * the export kept but could not resolve come back as
             * warnings.
             */
            async exportChildFlow(flowKey) {
                if (!this.selectedWorkflow) return;
                // A role:"component" child flow is a definition — its portable
                // form is the def envelope (ExportComponentDef), not the
                // node-membership child flow export.
                if (this.flowRoleFor(flowKey) === 'component') {
                    return this.exportComponentDef({ id: this.flowIdFor(flowKey) });
                }
                try {
                    if (this.dirty) await this.saveWorkflow();
                    const data = await this.api('/api/child-flow-export', {
                        id: this.selectedWorkflow.id,
                        childFlow: this.flowIdFor(flowKey)
                    });
                    const json = JSON.stringify(data.child_flow_export, null, 2);
                    const warnCount = (data.warnings || []).length;
                    (data.warnings || []).forEach(w => console.warn('child flow export:', w));
                    const note = warnCount ? ' — ' + warnCount + ' external-ref warning(s), see console' : '';
                    if (navigator.clipboard && navigator.clipboard.writeText) {
                        navigator.clipboard.writeText(json)
                            .then(() => this.showToast('Child flow export copied to clipboard' + note, warnCount ? 'warning' : 'info'))
                            .catch(() => this.fallbackCopyJson(json));
                    } else {
                        this.fallbackCopyJson(json);
                        if (warnCount) this.showToast('Child flow exported with ' + warnCount + ' warning(s)', 'warning');
                    }
                } catch (err) {
                    this.showToast('Child flow export failed: ' + err.message, 'danger');
                }
            },

            /**
             * Export a single component definition (the "Component
             * Definitions" list action): the same envelope, carrying
             * the def plus any defs it nests.
             */
            async exportComponentDef(def) {
                if (!this.selectedWorkflow || !def) return;
                try {
                    if (this.dirty) await this.saveWorkflow();
                    const data = await this.api('/api/child-flow-export', {
                        id: this.selectedWorkflow.id,
                        component: def.id
                    });
                    const json = JSON.stringify(data.child_flow_export, null, 2);
                    if (navigator.clipboard && navigator.clipboard.writeText) {
                        navigator.clipboard.writeText(json)
                            .then(() => this.showToast('Component export copied to clipboard', 'info'))
                            .catch(() => this.fallbackCopyJson(json));
                    } else {
                        this.fallbackCopyJson(json);
                    }
                } catch (err) {
                    this.showToast('Component export failed: ' + err.message, 'danger');
                }
            },

            /**
             * Open the "Import child flow" modal (tree group-header action):
             * pasting a child flow envelope merges it into THIS workflow as
             * a new child flow — unlike the toolbar Import, which replaces
             * the canvas or creates a whole new workflow.
             */
            openFlowImport() {
                if (!this.selectedWorkflow) {
                    this.showToast('Select a workflow first', 'info');
                    return;
                }
                this.flowImportJson = '';
                this.flowImportError = '';
                bootstrap.Modal.getOrCreateInstance(this.$refs.flowImportModal).show();
            },

            /**
             * Merge the pasted child flow envelope into the selected
             * workflow via the backend (fresh child flow + node ids,
             * deduped label), then re-render the canvas.
             */
            async confirmFlowImport() {
                let parsed;
                try {
                    parsed = JSON.parse(this.flowImportJson);
                } catch (err) {
                    this.flowImportError = 'Invalid JSON: ' + err.message;
                    return;
                }
                try {
                    // The backend merges into the stored document, so
                    // unsaved canvas edits must be persisted first.
                    if (this.dirty) await this.saveWorkflow();
                    const data = await this.api('/api/child-flow-import', {
                        id: this.selectedWorkflow.id,
                        child_flow_export: parsed
                    });
                    bootstrap.Modal.getInstance(this.$refs.flowImportModal).hide();
                    if (data.workflow) {
                        this.selectedWorkflow = data.workflow;
                        this.renderFlow(data.workflow);
                        this.applyView(data.workflow.view);
                        this.dirty = false;
                    }
                    const warnings = data.warnings || [];
                    warnings.forEach(w => console.warn('child flow import:', w));
                    this.showToast('Child flow imported' +
                        (warnings.length ? ' — ' + warnings.length + ' warning(s), see console' : ''),
                        warnings.length ? 'warning' : 'success');
                } catch (err) {
                    this.flowImportError = 'Import failed: ' + err.message;
                }
            },

            /**
             * Rename a child flow (tab or tree driven). The rename moves the
             * child flow object (meta included), so the stable meta.id — the
             * value nodes carry in data.childFlow — is untouched; only
             * meta.label follows the new name. No node rewrites needed.
             * Returns the new canvas key on success, null otherwise.
             */
            renameFlowByName(flowKey) {
                const current = this.flowLabelFor(flowKey);
                const kind = this.flowRoleFor(flowKey) === 'component' ? 'component' : 'child flow';
                const name = (window.prompt('Rename ' + kind, current) || '').trim();
                if (!name || name === flowKey) return null;
                if (this.editor.drawflow.drawflow[name]) {
                    this.showToast(`A ${kind} named "${name}" already exists`, 'error');
                    return null;
                }
                if (typeof this.editor.renameModule !== 'function' || !this.editor.renameModule(flowKey, name)) {
                    this.showToast('Rename failed', 'error');
                    return null;
                }
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(name, { label: name });
                }
                const tab = this.flowTabs.find(t => t.childFlow === flowKey);
                const wasActive = this.editor.module === flowKey || this.activeTab === flowKey;
                if (tab) {
                    tab.childFlow = name;
                    tab.label = name;
                    if (wasActive) this.switchTab(tab);
                } else if (wasActive) {
                    this.editor.changeModule(name);
                    this.activeTab = name;
                }
                this.knownFlows = this.knownFlows.map(m => (m === flowKey ? name : m));
                this.flowTreeVersion++;
                this.pushHistory();
                this.dirty = true;
                return name;
            },

            renameFlowTab(tab) {
                this.renameFlowByName(tab.childFlow);
            },

            /**
             * Enable/disable a child flow from the tree context menu.
             * The flag lives on meta.enabled (persisted via view.childFlows
             * and mirrored into view.disabledFlows on save); an open
             * tab's disabled flag follows it.
             */
            toggleFlowDisabled(flowKey) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? this.editor.getModuleMeta(flowKey) : null;
                const disabled = !(meta && meta.enabled === false); // now disabled?
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(flowKey, { enabled: !disabled });
                }
                const tab = this.flowTabs.find(t => t.childFlow === flowKey);
                if (tab) tab.disabled = disabled;
                this.refreshDisabledDom();
                this.flowTreeVersion++;
                this.dirty = true;
                this.pushHistory();
            },

            /**
             * Delete a child flow entirely — the only destructive child flow
             * action. Removes its nodes/edges from storage (the canvas
             * child flow is the export source), closes its tab if open.
             * The last child flow cannot be deleted.
             */
            deleteFlowByName(flowKey) {
                const df = this.editor.drawflow.drawflow || {};
                const names = Object.keys(df);
                if (names.length <= 1) {
                    this.showToast('Cannot delete the last child flow', 'info');
                    return;
                }
                const label = this.flowLabelFor(flowKey);
                const kind = this.flowRoleFor(flowKey) === 'component' ? 'component' : 'child flow';
                if (!confirm(`Delete ${kind} "${label}"? Its nodes and wires will be removed.`)) return;

                // If the doomed child flow is the visible one, activate a
                // survivor first — removeModule on the current child flow
                // leaves Drawflow pointed at a dead key.
                if (this.editor.module === flowKey) {
                    const survivor = names.find(m => m !== flowKey);
                    let survivorTab = this.flowTabs.find(t => t.childFlow === survivor);
                    if (!survivorTab) {
                        survivorTab = {
                            childFlow: survivor,
                            label: this.flowLabelFor(survivor),
                            role: this.flowRoleFor(survivor)
                        };
                        this.flowTabs.push(survivorTab);
                    }
                    this.editor.changeModule(survivor);
                    this.activeTab = survivor;
                }
                this.flowTabs = this.flowTabs.filter(t => t.childFlow !== flowKey);
                try {
                    this.editor.removeModule(flowKey);
                } catch (err) {
                    console.warn('Failed to delete childFlow:', err);
                }
                this.knownFlows = this.knownFlows.filter(m => m !== flowKey);
                this.flowTreeVersion++;
                this.refreshDisabledDom();
                this.pushHistory();
                this.dirty = true;
            },

            /**
             * Toggle a whole flow tab disabled (Node-RED "disabled":
             * true on the tab object). Persists as view.disabledFlows;
             * every node of the child flow deploys inert.
             */
            toggleFlowTabDisabled(tab) {
                tab.disabled = !tab.disabled;
                // Keep the child flow entity's enabled flag in sync so
                // view.childFlows reflects the same state as
                // view.disabledFlows.
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(tab.childFlow, { enabled: !tab.disabled });
                }
                this.refreshDisabledDom();
                this.flowTreeVersion++;
                this.dirty = true;
                this.pushHistory();
            },

            /**
             * Rebuild the open-tab list after an import / undo snapshot
             * restore: keep tabs whose child flow still exists and open any
             * child flow that appeared since the last sync (a restored or
             * freshly imported child flow). Child flows that were closed before
             * stay closed — the tree still lists them.
             */
            syncTabsFromEditor() {
                const childFlows = Object.keys((this.editor.drawflow && this.editor.drawflow.drawflow) || {});
                const prev = this.knownFlows || [];
                const appeared = childFlows.filter(m => prev.indexOf(m) === -1);
                this.flowTabs = childFlows
                    .filter(m => this.flowTabs.some(t => t.childFlow === m) || appeared.indexOf(m) !== -1)
                    .map(m => {
                        const tab = this.flowTabs.find(t => t.childFlow === m);
                        return {
                            childFlow: m,
                            label: tab ? tab.label : this.flowLabelFor(m),
                            role: this.flowRoleFor(m)
                        };
                    });
                this.knownFlows = childFlows.slice();
                this.activeTab = this.flowTabs.length ? this.editor.module : null;
                this.flowTreeVersion++;
            }
        },
        updated() {
            // Re-sync Bootlegance UI state after Vue patches (active
            // mode/font buttons); init skips nodes that carry a real
            // wired marker, so this is cheap and safe to run per render.
            if (window.Bootlegance && typeof window.Bootlegance.init === 'function') {
                window.Bootlegance.init();
            }
        },
        unmounted() {
            this.stopPolling();
            if (this.minimapTimer) clearTimeout(this.minimapTimer);
            if (this.historyDebounce) clearTimeout(this.historyDebounce);
            if (this._keyHandler) document.removeEventListener('keydown', this._keyHandler);
            if (this._popHandler) window.removeEventListener('popstate', this._popHandler);
        }
    });
    // Shared chrome: navbar comes from the AppNavigation component
    // (single parent layout). The component rewires Bootlegance in its
    // own mounted() hook.
    if (window.AppNavigation) {
        app.component('app-navigation', window.AppNavigation);
    }
    app.mount('#app');
    // Bootlegance runs its DOMContentLoaded init before Vue mounts, so it
    // wired the ORIGINAL DOM — Vue then clones those nodes into its
    // render, losing every listener while keeping the
    // data-bootlegance-wired markers. Strip cloned markers and the
    // cloned picker select once, then re-init so Bootlegance
    // recreates/wires the real rendered nodes.
    document.querySelectorAll('#app [data-bootlegance-wired]')
        .forEach(function (el) { el.removeAttribute('data-bootlegance-wired'); });
    document.querySelectorAll('#app [data-bootlegance-picker] select')
        .forEach(function (el) { el.remove(); });
    if (window.Bootlegance && typeof window.Bootlegance.init === 'function') {
        window.Bootlegance.init();
    }
}

document.addEventListener('DOMContentLoaded', function() {
    if (typeof Vue !== 'undefined' && typeof Drawflow !== 'undefined') {
        initEditorApp();
    } else {
        window.addEventListener('load', function() {
            if (typeof Vue !== 'undefined' && typeof Drawflow !== 'undefined') {
                initEditorApp();
            }
        });
    }
});
