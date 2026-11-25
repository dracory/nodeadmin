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
 * Subflow port marker node types (subflow_module_tree). These are
 * canvas-only pseudo nodes living inside role:"subflow" modules —
 * they never reach the backend node list. On save each marker becomes
 * one entry of the definition's in[]/out[] port arrays
 * (serializeSubflowDefs); on load the arrays are turned back into
 * markers (renderSubflowDef). The node's data.portIndex pins the port
 * order so dragging a marker never renumbers instance ports.
 */
const SUBFLOW_PORT_TYPES = [
    {
        id: 'subflowIn', name: 'Subflow input',
        description: 'Input port of this subflow definition',
        inputs: 0, outputs: 1, color: '#7e8ba0',
        icon: 'sign-in-alt', category: 'Subflow ports'
    },
    {
        id: 'subflowOut', name: 'Subflow output',
        description: 'Output port of this subflow definition',
        inputs: 1, outputs: 0, color: '#7e8ba0',
        icon: 'sign-out-alt', category: 'Subflow ports'
    }
];

/** True when a Drawflow node class is a subflow port marker. */
function isSubflowPortClass(cls) {
    return cls === 'subflowIn' || cls === 'subflowOut';
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
                nodeTypeSearch: '',
                // Palette grouping: collapsed state per category name,
                // session-scoped (all groups start expanded — pick noted
                // in the task: remembering state needs no persistence).
                paletteCollapsed: {},
                dirty: false, // unsaved changes on the canvas
                importJson: '',
                importError: '',
                // Module import modal (module_export_import): pasted
                // module envelope merged into this workflow.
                moduleImportJson: '',
                moduleImportError: '',
                // Subflow definitions are canvas modules now: each
                // type:"subflow" node of the workflow materializes as a
                // role:"subflow" module (subflow_module_tree) edited in
                // place with subflowIn/subflowOut port markers. The
                // subflowDefs computed derives the palette entries
                // ("subflow:<id>" instances) from those modules.
                // Flow env (view.env): Node-RED env-var table
                // [{name,type,value}] consulted by env.get for every
                // node in this workflow. Group env lives on the group
                // objects (view.groups[].env); global env is a
                // server-side singleton edited via the same modal.
                flowEnv: [],
                // Shared env-table editor: target is 'flow' | 'global' |
                // 'group' | 'module'; groupId is set for group edits,
                // moduleKey for subflow-module env edits.
                envEdit: { target: '', groupId: null, moduleKey: '', title: '', rows: [] },
                // Group properties modal (group_properties): edit state
                // for the group targeted by a label double-click;
                // members is a resolved read-only [{id, label}] list.
                groupEdit: { id: null, name: '', disabled: false, members: [] },
                // Flow tabs (task 11): each tab maps to a Drawflow module.
                // Since the module navigator landed, tabs are only the
                // OPEN modules (VS Code model) — the sidebar tree lists
                // every materialized module and is the source of truth.
                // Empty by default: no selected
                // workflow means no open tabs, and no open tabs means
                // the workspace is replaced by the welcome screen.
                flowTabs: [],
                activeTab: null,
                // Module navigator: knownModules is the last synced set of
                // canvas module keys (used to tell reopened vs. brand-new
                // modules apart in syncTabsFromEditor); moduleTreeVersion
                // is bumped whenever the module set/meta changes so the
                // non-reactive Drawflow model re-renders in the tree.
                knownModules: [],
                moduleTreeVersion: 0,
                // Right-click menu on a module tree item. The menu shape
                // mirrors the node context menu (Open/Rename/Export…/
                // Disable/Delete).
                moduleContextMenu: { show: false, x: 0, y: 0, module: '' },
                // Sidebar file tree (sidebar_file_tree): the workflow
                // list and module navigator merged into one Explorer
                // tree. treeGroups is the in-memory copy of
                // view.treeGroups — a flat list of folder objects
                // {id, name, parentId, modules:[moduleId]}; parentId
                // nests folders, module membership is by module id
                // (renames don't break it). Folders organize ONLY:
                // no deploy/env/disabled semantics.
                // collapsedTreeNodes tracks expanded state by row key
                // (session-only); treeDropKey highlights the drop
                // target during a module drag.
                treeGroups: [],
                collapsedTreeNodes: {},
                folderContextMenu: { show: false, x: 0, y: 0, folderId: null },
                treeDropKey: null,
                dragModuleKey: null,
                // Workflow groups (workflow_groups): top-level folders of
                // the Workflows super-group — flat list of
                // {id,name,parentId,workflows:[workflowId]} organizing
                // workflow DOCUMENTS (treeGroups organize modules inside
                // a workflow — different concept). Server-persisted via
                // /api/workflow-groups-* under the global-config
                // "workflowGroups" key; purely visual. dragWorkflowId
                // mirrors dragModuleKey for workflow-row drags.
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
                // "subflow:<id>" type entries built from the raw
                // definition nodes while a workflow renders — inner
                // instances resolve their def's port counts before the
                // def's own markers exist. Cleared when render ends.
                pendingSubflowTypes: {},
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
            // Subflow port markers are nodes — adding/removing one
            // changes a definition's port count, so recompute the
            // module-derived palette entries (subflowDefs computed
            // keys off moduleTreeVersion).
            ['nodeCreated', 'nodeRemoved', 'import']
                .forEach(ev => this.editor.on(ev, () => {
                    if (!this.suppressHistory) this.moduleTreeVersion++;
                }));
            // Node groups live in the editor's per-module model
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
                this.moduleContextMenu.show = false;
                this.folderContextMenu.show = false;
                this.workflowContextMenu.show = false;
                this.workflowGroupContextMenu.show = false;
            });

            // floweditor v0.6.0: module meta (label/role/enabled/env)
            // changes refresh the navigator tree.
            this.editor.on('moduleMetaChanged', () => { this.moduleTreeVersion++; });

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
             * Node types filtered by the palette search box (name/description)
             */
            filteredNodeTypes() {
                const q = (this.nodeTypeSearch || '').trim().toLowerCase();
                // Palette = core node types + one entry per subflow
                // definition (placed as a "subflow:<id>" instance).
                let all = this.nodeTypes.concat(this.subflowTypes);
                const inSubflow = this.activeModuleRole === 'subflow';
                if (inSubflow) {
                    // Port markers are only placeable inside a
                    // definition module; a definition must not
                    // instantiate itself (recursive expansion fails
                    // deploy).
                    const selfId = 'subflow:' + this.moduleIdFor(this.activeTab);
                    all = all.filter(t => t.id !== selfId).concat(SUBFLOW_PORT_TYPES);
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
             * only where matches exist. Subflow defs and subflow port
             * markers are just more categories.
             */
            groupedNodeTypes() {
                const order = [
                    'Common', 'Function', 'Actions', 'Network', 'Sequence',
                    'Parsers', 'Storage', 'Dashboard', 'Triggers', 'Queue',
                    'Document', 'Flow Control', 'AI', 'Subflows', 'Subflow ports'
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
             * One palette entry per subflow definition; placing it drops
             * a "subflow:<defId>" instance node on the canvas. Ports come
             * from the definition's in/out arrays.
             */
            subflowTypes() {
                return (this.subflowDefs || []).map(d => {
                    const data = d.data || {};
                    const ins = Array.isArray(data.in) ? data.in.length : 0;
                    const outs = Array.isArray(data.out) ? data.out.length : 0;
                    return {
                        id: 'subflow:' + d.id,
                        name: 'subflow: ' + (data.name || d.id),
                        description: 'Instance of subflow "' + (data.name || d.id) + '"',
                        icon: 'object-group',
                        category: 'Subflows',
                        inputs: ins,
                        outputs: outs,
                        color: '#7e8ba0'
                    };
                });
            },

            /**
             * Groups across all modules in the persisted view.groups
             * shape (flat array with a module field). Read-only view
             * over the editor's per-module model.
             */
            groups() {
                return this.serializeGroups();
            },

            /**
             * Module navigator tree: every materialized module (open in a
             * tab or not), split into the Flows and Subflows groups the
             * sidebar renders. Drawflow's module model is not reactive —
             * moduleTreeVersion is bumped by every mutation path
             * (render/add/delete/rename/meta change) to recompute this.
             */
            moduleTree() {
                void this.moduleTreeVersion; // reactivity dependency
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const items = Object.keys(df).map(name => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(name) || {}) : {};
                    return {
                        module: name,
                        id: meta.id || name,
                        label: this.moduleLabelFor(name),
                        role: meta.role || 'flow',
                        enabled: meta.enabled !== false,
                        open: this.flowTabs.some(t => t.module === name)
                    };
                });
                return {
                    flows: items.filter(m => m.role !== 'subflow'),
                    subflows: items.filter(m => m.role === 'subflow')
                };
            },

            /** Canvas-name lookup over the flat module list. */
            moduleItemsByName() {
                const map = {};
                this.moduleTree.flows.concat(this.moduleTree.subflows)
                    .forEach(m => { map[m.module] = m; });
                return map;
            },

            /**
             * Sidebar super-groups (sidebar_super_groups): the Explorer
             * is split into two fixed UI sections — "Workflows" and
             * "Subflows" — like VSCode's split Explorer panes (e.g.
             * Open Editors / Files). Section membership is by kind,
             * not by folder: role:"subflow" modules always render
             * under Subflows even when a folder claims them (folders
             * stay organization-only and only group flow modules).
             *
             * workflowTreeRows keeps the sidebar_file_tree arrangement
             * for the Workflows section: root rows are workflow groups
             * (workflowGroups, nested via parentId) and ungrouped
             * workflows — a workflow renders inside its group, the rest
             * form the tail. Only the SELECTED workflow expands into
             * children, because module leaves live inside the loaded
             * workflow document (other workflows' modules only exist
             * after selectWorkflow fetches them). Children of the
             * selected workflow are user folders (view.treeGroups,
             * nested via parentId) followed by ungrouped flow-module
             * leaves; modules inside a folder render under it. Stale
             * group/folder refs and duplicate membership (first group
             * wins) degrade gracefully to the ungrouped flat list.
             */
            workflowTreeRows() {
                void this.moduleTreeVersion; // reactivity dependency
                const rows = [];
                const items = this.moduleItemsByName;
                const emitChildren = (depth) => {
                    // Module canvas names claimed by any folder — kept
                    // out of the ungrouped tail below.
                    const claimed = {};
                    this.treeGroups.forEach(g => (g.modules || []).forEach(mid => {
                        claimed[this.resolveModuleName(String(mid))] = true;
                    }));
                    const emitModule = (m, depth) => rows.push({
                        key: 'mod:' + m.module, type: 'module',
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
                                (g.modules || []).forEach(mid => {
                                    const m = items[this.resolveModuleName(String(mid))];
                                    // Kind wins over folder membership:
                                    // subflow modules render under the
                                    // Subflows super-group instead.
                                    if (!m || m.role === 'subflow') return;
                                    // First folder wins on duplicate
                                    // membership — never emit twice.
                                    if (!emitted[m.module]) {
                                        emitted[m.module] = true;
                                        emitModule(m, depth + 1);
                                    }
                                });
                                emitFolders(g.id, depth + 1);
                            });
                    };
                    emitFolders(null, depth);
                    this.moduleTree.flows
                        .filter(m => !claimed[m.module])
                        .forEach(m => emitModule(m, depth));
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
             * Rows for the "Subflows" super-group: the SELECTED
             * workflow's role:"subflow" definition modules, flat at
             * depth 0 under the section header. Subflow definitions
             * live inside the workflow document — there are no global
             * subflows — so the section is empty until a workflow is
             * selected. Folder membership never hides a subflow here
             * (kind beats organization).
             */
            subflowTreeRows() {
                void this.moduleTreeVersion; // reactivity dependency
                if (!this.selectedWorkflow) return [];
                return this.moduleTree.subflows.map(m => ({
                    key: 'mod:' + m.module, type: 'module',
                    item: m, label: m.label, depth: 0
                }));
            },

            /**
             * All sidebar rows (Workflows section + Subflows section),
             * kept for tests and external consumers that want the full
             * flattened tree.
             */
            treeRows() {
                return this.workflowTreeRows.concat(this.subflowTreeRows);
            },

            /**
             * Role of the module on the visible canvas ('flow' |
             * 'subflow'); used by the palette to offer port markers
             * only inside definition modules.
             */
            activeModuleRole() {
                return this.activeTab ? this.moduleRoleFor(this.activeTab) : 'flow';
            },

            /**
             * Subflow definitions derived from the materialized
             * role:"subflow" canvas modules (subflow_module_tree —
             * definitions became modules; the type:"subflow" node is
             * only the serialization format). Each entry mirrors the
             * old def shape ({id, data:{name,in,out}}) the palette and
             * export paths consume; in/out are length-only placeholders
             * built from the module's port markers.
             */
            subflowDefs() {
                void this.moduleTreeVersion; // reactivity dependency
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const defs = [];
                Object.keys(df).forEach(name => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(name) || {}) : {};
                    if ((meta.role || 'flow') !== 'subflow') return;
                    const flow = df[name].data || {};
                    let ins = 0;
                    let outs = 0;
                    Object.keys(flow).forEach(id => {
                        if (flow[id].class === 'subflowIn') ins++;
                        else if (flow[id].class === 'subflowOut') outs++;
                    });
                    defs.push({
                        id: meta.id || name,
                        module: name,
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
             * subflow palette entries (for "subflow:<defId>" ids).
             */
            findType(typeId) {
                return this.nodeTypes.find(t => t.id === typeId) ||
                    this.pendingSubflowTypes[typeId] ||
                    this.subflowTypes.find(t => t.id === typeId) ||
                    SUBFLOW_PORT_TYPES.find(t => t.id === typeId);
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
                    this.resetModules();
                    this.drawflowToBackend = {};
                    this.backendToDrawflow = {};

                    // Subflow definitions live in the nodes list as
                    // type:"subflow" but are templates: each becomes a
                    // role:"subflow" canvas module (subflow_module_tree)
                    // rendered by renderSubflowDef below.
                    const subflowDefNodes = (workflow.nodes || []).filter(n => n.type === 'subflow');
                    const subflowDefIds = {};
                    subflowDefNodes.forEach(d => { subflowDefIds[String(d.id)] = true; });

                    // Materialize persisted module entities first
                    // (view.modules): each becomes a canvas module
                    // keyed by its label with its stable meta.id
                    // restored, so node data.module=id references keep
                    // pointing at the same module across save/load.
                    // A role:"subflow" entity whose id matches a
                    // definition node is skipped — the definition
                    // materializes its own module below; an orphan
                    // entity (imported shell with no def) still gets a
                    // module so nothing silently vanishes.
                    const savedModules = (workflow.view && Array.isArray(workflow.view.modules))
                        ? workflow.view.modules : [];
                    savedModules.forEach(m => {
                        if (!m || !m.id) return;
                        if (m.role === 'subflow' && subflowDefIds[String(m.id)]) return;
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

                    // Materialize each subflow definition as a
                    // role:"subflow" canvas module holding its inner
                    // graph plus in/out port markers. Two passes: every
                    // module entity must exist (with its port-counting
                    // meta) before inner graphs render, so a nested
                    // "subflow:<id>" instance resolves its def's ports
                    // regardless of definition order.
                    this.pendingSubflowTypes = {};
                    subflowDefNodes.forEach(d => {
                        const data = d.data || {};
                        this.pendingSubflowTypes['subflow:' + d.id] = {
                            id: 'subflow:' + d.id,
                            name: 'subflow: ' + (data.name || d.id),
                            inputs: Array.isArray(data.in) ? data.in.length : 0,
                            outputs: Array.isArray(data.out) ? data.out.length : 0,
                            color: '#7e8ba0',
                            icon: 'object-group',
                            category: 'Subflows'
                        };
                    });
                    const defModuleKeys = subflowDefNodes.map(d => this.prepareSubflowDefModule(d));
                    subflowDefNodes.forEach((d, i) => this.populateSubflowDefModule(d, defModuleKeys[i]));

                    // Group nodes by their tab. data.module stores the
                    // module id; legacy workflows store the module
                    // name — resolve either to the canvas module key.
                    const groups = {}; // canvas module name -> [backend nodes]
                    (workflow.nodes || []).forEach(node => {
                        if (node.type === 'subflow') return;
                        const mod = this.resolveModuleName(
                            (node.data && node.data.module) || 'Home');
                        (groups[mod] = groups[mod] || []).push(node);
                    });
                    const moduleNames = Object.keys(groups);
                    if (!moduleNames.length) moduleNames.push('Home');
                    moduleNames.forEach(m => {
                        if (!this.editor.drawflow.drawflow[m]) this.editor.addModule(m);
                    });
                    // Tabs cover every materialized canvas module —
                    // populated ones first, then node-less modules
                    // restored from view.modules.
                    const savedLabels = savedModules
                        .filter(m => m && m.id)
                        .map(m => m.label || 'Home');
                    const ordered = moduleNames
                        .concat(savedLabels.filter(n => moduleNames.indexOf(n) === -1))
                        .concat(defModuleKeys.filter(n => moduleNames.indexOf(n) === -1 && savedLabels.indexOf(n) === -1));
                    // Drop canvas modules not backed by nodes or a
                    // saved entity — the leftover kept by resetModules
                    // (e.g. 'Home' after the workflow's Home module was
                    // renamed) must not resurface as a phantom tab.
                    const wanted = {};
                    ordered.forEach(n => { wanted[n] = true; });
                    Object.keys(this.editor.drawflow.drawflow || {}).forEach(n => {
                        if (!wanted[n]) {
                            try { this.editor.removeModule(n); } catch (err) { /* ignore */ }
                        }
                    });
                    // Tabs are only the OPEN modules (module navigator):
                    // every module stays materialized in Drawflow and in
                    // the tree, but the tab bar shows the persisted open
                    // set — view.openModules (module ids). Documents
                    // without the key predate the navigator and open every
                    // module, matching the old 1:1 tab behavior.
                    const openIds = (workflow.view && Array.isArray(workflow.view.openModules))
                        ? workflow.view.openModules.map(v => this.resolveModuleName(String(v)))
                        : null;
                    const openNames = openIds
                        ? ordered.filter(m => openIds.indexOf(m) !== -1)
                        // Documents predating the navigator open every
                        // flow module; definition modules stay closed
                        // (opened from the tree) so a load does not
                        // spawn a tab per subflow.
                        : ordered.filter(m => this.moduleRoleFor(m) !== 'subflow');
                    if (!openNames.length && ordered.length) openNames.push(ordered[0]);
                    this.flowTabs = openNames.map(m => ({
                        module: m,
                        label: this.moduleLabelFor(m),
                        role: this.moduleRoleFor(m)
                    }));
                    this.knownModules = ordered.slice();
                    this.moduleTreeVersion++;

                    moduleNames.forEach(mod => {
                        this.editor.changeModule(mod);
                        const idMap = {}; // backend node id -> Drawflow node id (within this module)
                        (groups[mod] || []).forEach(node => {
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
                        // Edges only apply when both endpoints live in this module
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
                    this.editor.changeModule(this.flowTabs[0].module);
                    this.activeTab = this.flowTabs[0].module;
                } finally {
                    this.suppressHistory = false;
                    this.pendingSubflowTypes = {};
                }
                // Per-workflow history: clear stacks on workflow switch (task 09)
                this.clearHistory();
                this.refreshAllOutputLabels();
                this.scheduleMinimap();
            },

            /**
             * Remove all nodes from every module and delete extra modules,
             * leaving a single empty module named 'Home'
             */
            resetModules() {
                const modules = Object.keys(this.editor.drawflow.drawflow || {});
                modules.forEach(m => {
                    this.editor.changeModule(m);
                    this.editor.clearModuleSelected();
                });
                const keep = modules.indexOf('Home') !== -1 ? 'Home' : modules[0];
                if (keep) {
                    this.editor.changeModule(keep);
                    modules.forEach(m => {
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
             * Pass 1 of definition materialization (subflow_module_tree):
             * create the role:"subflow" canvas module and its meta for
             * one type:"subflow" node. The module key is the def name
             * deduped against existing canvas modules; the def id
             * becomes meta.id so "subflow:<id>" instance types keep
             * resolving. Returns the canvas module key.
             */
            prepareSubflowDefModule(def) {
                const data = def.data || {};
                const df = this.editor.drawflow.drawflow;
                const base = String(data.name || '').trim() || 'Subflow';
                let key = base;
                for (let i = 2; df[key]; i++) key = base + ' ' + i;
                this.editor.addModule(key);
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(key, {
                        id: String(def.id),
                        label: base,
                        role: 'subflow',
                        enabled: true,
                        env: Array.isArray(data.env) ? data.env : []
                    });
                }
                return key;
            },

            /**
             * Pass 2 of definition materialization: fill the module
             * created by prepareSubflowDefModule with the inner
             * nodes/edges plus one subflowIn/subflowOut marker node per
             * in[]/out[] port.
             */
            populateSubflowDefModule(def, key) {
                const data = def.data || {};
                this.editor.changeModule(key);

                const idMap = {}; // persisted id -> drawflow id (within this module)
                const portX = (p, fallback) => (p && typeof p.x === 'number') ? p.x : fallback;
                const portY = (p, fallback) => (p && typeof p.y === 'number') ? p.y : fallback;

                // Inner nodes accept both the backend shape
                // ({id,type,position:{x,y},data:{...}}) and the Node-RED
                // flat shape ({id,type,x,y,...config}) — the same
                // contract as the backend's parseSubflowInnerNode.
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
                        'subflowIn', 'input ' + (i + 1),
                        portX(p, 40), portY(p, 40 + i * 60),
                        { name: 'input ' + (i + 1), portIndex: i });
                });
                outs.forEach((p, i) => {
                    idMap['__out_' + i] = this.addNode(
                        'subflowOut', 'output ' + (i + 1),
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

            /* ---- Module identity (id-keyed data.module migration) ---- */

            /**
             * Resolve a stored data.module reference — a module id
             * (current model) or a legacy module name — to the Drawflow
             * canvas module key. Canvas keys stay names; unresolvable
             * refs return themselves so the caller can still
             * materialize a module for them.
             */
            resolveModuleName(ref) {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                if (df[ref]) return ref; // already a canvas module key
                if (this.editor && typeof this.editor.getModuleById === 'function') {
                    const name = this.editor.getModuleById(ref);
                    if (name) {
                        console.debug('Resolved module id to canvas module:', ref, '->', name);
                        return name;
                    }
                }
                return ref;
            },

            /**
             * Stable module id for a canvas module name (meta.id),
             * falling back to the name itself when the fork lacks meta.
             */
            moduleIdFor(name) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? this.editor.getModuleMeta(name) : null;
                return (meta && meta.id) || name;
            },

            /**
             * Display label for a canvas module: meta.label when the
             * fork carries it, with the legacy Home -> Main cosmetic
             * rename preserved.
             */
            moduleLabelFor(name) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? this.editor.getModuleMeta(name) : null;
                const label = (meta && meta.label) || name;
                return name === 'Home' ? 'Main' : label;
            },

            /**
             * Module role ('flow' | 'subflow') from meta; defaults to
             * 'flow' so every module sorts into a tree group.
             */
            moduleRoleFor(name) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? this.editor.getModuleMeta(name) : null;
                return (meta && meta.role) || 'flow';
            },

            /**
             * Module entities persisted as view.modules: the
             * {id, label, role, enabled, env} meta of every canvas
             * module, so ids survive save/load and module state
             * (role/enabled/env) outlives node membership.
             */
            serializeModules() {
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
                const modules = (exported && exported.drawflow) || {};
                const nodes = [];
                const edges = [];
                // Merge every Drawflow module (tab) into the single backend
                // nodes+edges set. data.module carries the stable module
                // id (meta.id); data.moduleName is a denormalized display
                // label so stored documents stay human-readable. Canvas
                // module keys remain names — Drawflow keys modules by name.
                Object.keys(modules).forEach(modName => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(modName) || {}) : {};
                    // Subflow template modules serialize as type:"subflow"
                    // definition nodes (serializeSubflowDefs), never as
                    // runnable nodes.
                    if ((meta.role || 'flow') === 'subflow') return;
                    const flow = modules[modName].data || {};
                    const markerIds = {};
                    Object.keys(flow).forEach(id => {
                        if (isSubflowPortClass(flow[id].class)) markerIds[id] = true;
                    });
                    Object.keys(flow).forEach(id => {
                        const n = flow[id];
                        // Defensive: port markers only belong inside
                        // subflow modules; one stranded in a flow
                        // module must not reach the backend as an
                        // unknown type.
                        if (markerIds[id]) return;
                        const data = Object.assign({}, n.data || {});
                        data.module = meta.id || modName;
                        if (meta.label) data.moduleName = meta.label;
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
                                    id: 'edge-' + modName + '-' + id + '-' + outputKey + '-' + conn.node + '-' + conn.output,
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
             * Serialize every role:"subflow" canvas module back into a
             * type:"subflow" definition node (subflow_module_tree) —
             * the inverse of renderSubflowDef. Port markers become
             * in[]/out[] entries ordered by their data.portIndex; the
             * remaining nodes/edges become the definition's internal
             * graph. The def id is the module's stable meta.id so
             * "subflow:<id>" instances keep resolving across save/load.
             */
            serializeSubflowDefs() {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const defs = [];
                Object.keys(df).forEach(name => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(name) || {}) : {};
                    if ((meta.role || 'flow') !== 'subflow') return;
                    const flow = df[name].data || {};
                    const markerIds = {}; // drawflow id -> 'in' | 'out'
                    const markers = { in: [], out: [] };
                    Object.keys(flow).forEach(id => {
                        const cls = flow[id].class;
                        if (cls === 'subflowIn') {
                            markerIds[id] = 'in';
                            markers.in.push({ id: id, node: flow[id] });
                        } else if (cls === 'subflowOut') {
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
                        delete data.module;
                        delete data.moduleName;
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
                        type: 'subflow',
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
                const modules = (copy && copy.drawflow) || {};
                Object.keys(modules).forEach(mod => {
                    const data = (modules[mod] && modules[mod].data) || {};
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
             * Resize an errorCatch node's output ports when the flag
             * toggles — same surgery as syncSwitchOutputs.
             */
            syncErrorCatchOutputs(nodeId) {
                const node = this.editor.getNodeFromId(nodeId);
                if (!node) return;
                const type = this.findType(node.data && node.data.type) ||
                    { id: node.data && node.data.type, outputs: 1 };
                const desired = this.errorCatchOutputCount(type, node.data);
                const current = Object.keys(node.outputs || {}).length;
                if (desired === current) {
                    this.refreshOutputLabels(nodeId);
                    return;
                }
                if (typeof this.editor.addNodeOutput !== 'function' ||
                    typeof this.editor.removeNodeOutput !== 'function') {
                    console.error('syncErrorCatchOutputs: Drawflow build lacks addNodeOutput/removeNodeOutput');
                    if (typeof this.showToast === 'function') {
                        this.showToast('Could not update node ports — save and reload the page');
                    }
                    return;
                }
                Object.keys(node.outputs || {}).forEach(outputKey => {
                    const idx = parseInt(String(outputKey).replace('output_', ''), 10);
                    if (idx > desired) {
                        ((node.outputs[outputKey] && node.outputs[outputKey].connections) || [])
                            .forEach(c => this.editor.removeSingleConnection(
                                nodeId, c.node, outputKey, c.output));
                    }
                });
                for (let i = current + 1; i <= desired; i++) this.editor.addNodeOutput(nodeId);
                for (let i = current; i > desired; i--) this.editor.removeNodeOutput(nodeId, 'output_' + i);
                this.refreshOutputLabels(nodeId);
            },

            /**
             * Resize a checkpoint node's output ports to match its
             * routes config — same surgery as syncSwitchOutputs.
             */
            syncCheckpointOutputs(nodeId) {
                const node = this.editor.getNodeFromId(nodeId);
                if (!node) return;
                const desired = this.checkpointOutputCount(node.data);
                const current = Object.keys(node.outputs || {}).length;
                if (desired === current) {
                    this.refreshOutputLabels(nodeId);
                    return;
                }
                if (typeof this.editor.addNodeOutput !== 'function' ||
                    typeof this.editor.removeNodeOutput !== 'function') {
                    console.error('syncCheckpointOutputs: Drawflow build lacks addNodeOutput/removeNodeOutput');
                    if (typeof this.showToast === 'function') {
                        this.showToast('Could not update node ports — save and reload the page');
                    }
                    return;
                }
                Object.keys(node.outputs || {}).forEach(outputKey => {
                    const idx = parseInt(String(outputKey).replace('output_', ''), 10);
                    if (idx > desired) {
                        ((node.outputs[outputKey] && node.outputs[outputKey].connections) || [])
                            .forEach(c => this.editor.removeSingleConnection(
                                nodeId, c.node, outputKey, c.output));
                    }
                });
                for (let i = current + 1; i <= desired; i++) this.editor.addNodeOutput(nodeId);
                for (let i = current; i > desired; i--) this.editor.removeNodeOutput(nodeId, 'output_' + i);
                this.refreshOutputLabels(nodeId);
            },

            /**
             * Resize a switch node's output ports to match its rules +
             * otherwise config. Connections on ports beyond the new
             * count are dropped first; Drawflow's addNodeOutput /
             * removeNodeOutput do the DOM/graph surgery in place.
             */
            syncSwitchOutputs(nodeId) {
                const node = this.editor.getNodeFromId(nodeId);
                if (!node) return;
                const desired = this.switchOutputCount(node.data);
                const current = Object.keys(node.outputs || {}).length;
                if (desired === current) {
                    this.refreshOutputLabels(nodeId);
                    return;
                }
                if (typeof this.editor.addNodeOutput !== 'function' ||
                    typeof this.editor.removeNodeOutput !== 'function') {
                    console.error('syncSwitchOutputs: Drawflow build lacks addNodeOutput/removeNodeOutput');
                    if (typeof this.showToast === 'function') {
                        this.showToast('Could not update node ports — save and reload the page');
                    }
                    return;
                }
                Object.keys(node.outputs || {}).forEach(outputKey => {
                    const idx = parseInt(String(outputKey).replace('output_', ''), 10);
                    if (idx > desired) {
                        ((node.outputs[outputKey] && node.outputs[outputKey].connections) || [])
                            .forEach(c => this.editor.removeSingleConnection(
                                nodeId, c.node, outputKey, c.output));
                    }
                });
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
                // Subflow port markers: assign the next port index
                // within the current module when none is given, so the
                // serialized in[]/out[] order is stable regardless of
                // where the marker is dropped.
                if (isSubflowPortClass(type.id) &&
                    (nodeData.portIndex === undefined || nodeData.portIndex === null)) {
                    const mod = (this.editor.drawflow.drawflow || {})[this.editor.module];
                    let maxPort = -1;
                    Object.keys((mod && mod.data) || {}).forEach(k => {
                        const n = mod.data[k];
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
             * flat array carrying the module name, flattened from the
             * editor's per-module model. Members are filtered to live
             * nodes so a phantom group never renders as a box at (0,0).
             */
            serializeGroups() {
                const out = [];
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                Object.keys(df).forEach(mod => {
                    const data = df[mod].data || {};
                    Object.values(df[mod].groups || {}).forEach(g => {
                        const nodeIds = (g.nodeIds || []).filter(id => data[id]);
                        if (!nodeIds.length) return;
                        const entry = {
                            id: g.id,
                            // Module id, matching node data.module.
                            module: this.moduleIdFor(mod),
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
             * group's module (kept for e2e coverage; ids are unified
             * now that groups live in the editor model).
             */
            memberDrawflowId(gid) {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                for (const mod of Object.keys(df)) {
                    const groups = df[mod].groups || {};
                    for (const g of Object.values(groups)) {
                        if ((g.nodeIds || []).indexOf(String(gid)) !== -1) {
                            return (df[mod].data || {})[gid] ? String(gid) : null;
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
             * module — drives context menu visibility.
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
                const mod = this.editor.drawflow.drawflow[this.editor.module];
                const g = mod && mod.groups ? mod.groups[edit.id] : null;
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
             * Recompute the dimmed state of every node in every module:
             * a node dims when it is disabled itself, sits on a disabled
             * tab, or belongs to a disabled group.
             */
            refreshDisabledDom() {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                Object.keys(df).forEach(mod => {
                    // Closed modules have no tab — their disabled state
                    // comes straight from meta.enabled (the tree toggle
                    // and applyView both write it there).
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? this.editor.getModuleMeta(mod) : null;
                    const tabDisabled = this.flowTabs.some(t => t.module === mod && t.disabled) ||
                        (meta && meta.enabled === false);
                    const disabledGroups = Object.values(df[mod].groups || {})
                        .filter(g => g.disabled === true);
                    const data = df[mod].data || {};
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
             * The group containing a node in the active module (or null).
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
                const mod = this.editor.drawflow.drawflow[this.editor.module];
                if (mod && mod.groups && mod.groups[g.id]) mod.groups[g.id].disabled = disabled;
                g.disabled = disabled;
                if (typeof this.editor.updateGroup === 'function') {
                    try { this.editor.updateGroup(g.id, { disabled: disabled }); } catch (e) { /* optional */ }
                }
                this.refreshDisabledDom();
                this.dirty = true;
                this.pushHistory();
            },

            /* ---- Env tables (env.get chain: group > subflow > flow > global > OS) ---- */

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
             * id across modules — used by the group properties modal.
             */
            openGroupEnvById(groupId) {
                const df = this.editor.drawflow.drawflow || {};
                let g = null;
                for (const mod of Object.keys(df)) {
                    g = (df[mod].groups || {})[groupId];
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
             * Open the shared env editor for a role:"subflow" module:
             * the rows persist as meta.env and serialize into the
             * definition's env[] on save (the env.get chain's subflow
             * level).
             */
            openModuleEnv(moduleKey) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? (this.editor.getModuleMeta(moduleKey) || {}) : {};
                this.envEdit = {
                    target: 'module',
                    groupId: null,
                    moduleKey: moduleKey,
                    title: 'Subflow Environment Variables — ' + (meta.label || moduleKey),
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
                    for (const mod of Object.keys(df)) {
                        const g = (df[mod].groups || {})[edit.groupId];
                        if (g) {
                            g.env = rows;
                            this.dirty = true;
                            this.pushHistory();
                            break;
                        }
                    }
                    return;
                }
                if (edit.target === 'module') {
                    // Subflow-module env lives on meta.env and serializes
                    // into the definition's env[] on save.
                    if (typeof this.editor.setModuleMeta === 'function') {
                        this.editor.setModuleMeta(edit.moduleKey, { env: rows });
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
                    // Module entities {id,label,role,enabled,env} — the
                    // stable ids node data.module and the references
                    // below point at.
                    modules: this.serializeModules(),
                    // Disabled modules (Node-RED "disabled": true on
                    // the tab object): module ids whose nodes deploy
                    // inert (legacy documents stored names; the engine
                    // resolves both through view.modules). Covers closed
                    // modules too — their flag lives on meta.enabled.
                    disabledModules: Object.keys(this.editor.drawflow.drawflow || {})
                        .filter(m => {
                            const tab = this.flowTabs.find(t => t.module === m);
                            const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                                ? this.editor.getModuleMeta(m) : null;
                            return (tab && tab.disabled === true) ||
                                (meta && meta.enabled === false);
                        })
                        .map(m => this.moduleIdFor(m)),
                    // Open-tab set (module navigator): module ids of the
                    // modules with a tab, so a close survives reload.
                    openModules: this.flowTabs.map(t => this.moduleIdFor(t.module)),
                    // Sidebar folders (sidebar_file_tree): pure
                    // organization state — [{id,name,parentId,
                    // modules:[moduleId]}]; carries no runtime meaning.
                    treeGroups: this.serializeTreeGroups()
                };
            },

            /**
             * Restore a saved zoom/pan view state (if present)
             */
            applyView(view) {
                // Sidebar folder state (view.treeGroups): a flat list
                // of {id,name,parentId,modules:[moduleId]}. Missing or
                // malformed entries degrade to the ungrouped flat
                // list; stale module refs are skipped at render.
                const tg = (view && Array.isArray(view.treeGroups)) ? view.treeGroups : [];
                this.treeGroups = tg
                    .filter(g => g && g.id)
                    .map(g => ({
                        id: String(g.id),
                        name: String(g.name || 'Folder'),
                        parentId: g.parentId ? String(g.parentId) : null,
                        modules: Array.isArray(g.modules) ? g.modules.map(String) : []
                    }));
                this.collapsedTreeNodes = {};
                this.folderContextMenu.show = false;
                // Migrate persisted view.groups (flat array, module
                // field) into the editor's per-module group model.
                // Members that no longer resolve are dropped; a group
                // left with no members is skipped entirely.
                const saved = (view && Array.isArray(view.groups)) ? view.groups : [];
                const df = this.editor.drawflow.drawflow || {};
                saved.forEach(g => {
                    // g.module stores a module id (or a legacy name).
                    const mod = this.resolveModuleName(g.module || 'Home');
                    const m = df[mod];
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
                // Disabled flow tabs persist as view.disabledModules
                // (module ids; legacy documents stored names) — restore
                // the flag onto the tab model. A module entity with
                // enabled:false disables the tab too.
                const dm = (view && Array.isArray(view.disabledModules))
                    ? view.disabledModules.map(v => this.resolveModuleName(String(v))) : [];
                // Apply to every module, open or closed: tabs carry a
                // disabled flag for the styling, meta.enabled is the
                // persisted flag the tree and deploy read.
                Object.keys(this.editor.drawflow.drawflow || {}).forEach(m => {
                    const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                        ? this.editor.getModuleMeta(m) : null;
                    const disabled = dm.indexOf(m) !== -1 ||
                        (meta && meta.enabled === false);
                    const tab = this.flowTabs.find(t => t.module === m);
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

            /* ---- Subflows (role:"subflow" modules + "subflow:<id>" instances) ---- */

            /**
             * Create a subflow definition as a role:"subflow" canvas
             * module (subflow_module_tree) and open it in a tab. The
             * default inner graph is one input marker wired through a
             * passthrough junction to one output marker, so an instance
             * works out of the box; the user edits the inner graph and
             * ports right on the canvas.
             */
            createSubflow() {
                if (!this.selectedWorkflow) return;
                const name = prompt('Subflow name', 'Subflow');
                if (name === null) return;
                const df = this.editor.drawflow.drawflow || {};
                const base = String(name || '').trim() || 'Subflow';
                let key = base;
                for (let i = 2; df[key]; i++) key = base + ' ' + i;
                this.editor.addModule(key);
                const defId = uuidV7();
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(key, {
                        id: defId,
                        label: base,
                        role: 'subflow',
                        enabled: true,
                        env: []
                    });
                }
                this.editor.changeModule(key);
                const inId = this.addNode('subflowIn', 'input 1', 40, 40,
                    { name: 'input 1', portIndex: 0 });
                const innerId = this.addNode('junction', 'passthrough', 220, 40,
                    { name: 'passthrough' });
                const outId = this.addNode('subflowOut', 'output 1', 420, 40,
                    { name: 'output 1', portIndex: 0 });
                try {
                    this.editor.addConnection(inId, innerId, 'output_1', 'input_1');
                    this.editor.addConnection(innerId, outId, 'output_1', 'input_1');
                } catch (err) { /* default wiring is best-effort */ }

                const tab = { module: key, label: base, role: 'subflow' };
                this.flowTabs.push(tab);
                this.knownModules.push(key);
                this.moduleTreeVersion++;
                this.switchTab(tab);
                this.pushHistory();
                this.dirty = true;
                this.showToast('Subflow created — it now appears in Add Node; Save to persist', 'info');
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
                // to the backend converter (subflow definitions and
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
                const moduleData = (this.editor.drawflow.drawflow[this.editor.module] || {}).data || {};
                const adjacency = {};
                Object.keys(moduleData).forEach(id => {
                    adjacency[String(id)] = [];
                    Object.keys(moduleData[id].outputs || {}).forEach(outputKey => {
                        (moduleData[id].outputs[outputKey].connections || []).forEach(c => {
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
                if (prop && Array.isArray(prop.enum)) return 'select';
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
             * Peer nodes of a link type across all modules — Node-RED's
             * link dialogs list every candidate in the flow.
             * Returns [{id, label}] sorted by label.
             */
            linkPeerNodes(peerType) {
                const out = [];
                const modules = (this.editor.export().drawflow) || {};
                Object.keys(modules).forEach(mod => {
                    const data = (modules[mod] && modules[mod].data) || {};
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
                            label: mod === 'Home' ? label : label + ' [' + mod + ']'
                        });
                    });
                });
                out.sort((a, b) => a.label.localeCompare(b.label));
                return out;
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
                    let options = Array.isArray(prop.enum) ? prop.enum : null;
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
                    } else if (field === 'json') {
                        value = JSON.stringify(value, null, 2);
                    }

                    fields.push({
                        key,
                        title: prop.title || key,
                        description: prop.description || '',
                        field,
                        options,
                        required: required.indexOf(key) !== -1,
                        // Conditional visibility (showIf / visible-when):
                        // {otherKey: expectedValue, ...} — the field is
                        // shown only when every listed form value equals
                        // its expected value (e.g. {backend: 'dir'}).
                        showIf: prop.showIf || prop['visible-when'] || null
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
            schemaFieldVisible(field) {
                if (!field || !field.showIf) return true;
                return Object.keys(field.showIf).every(k =>
                    String(this.editingNodeForm[k]) === String(field.showIf[k]));
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
                    })
                    .catch(() => { /* keep JSON fallback */ });
            },

            /**
             * Persist edited node properties back into the Drawflow node
             */
            saveNodeProperties() {
                let data;
                if (this.editingNodeSchema) {
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
                if (this.editingNodeType === 'switch') {
                    this.syncSwitchOutputs(this.editingNodeId);
                }
                if (this.editingNodeType === 'checkpoint') {
                    this.syncCheckpointOutputs(this.editingNodeId);
                }
                if (errorCatchType) {
                    this.syncErrorCatchOutputs(this.editingNodeId);
                }
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
                // Subflow definitions are canvas modules now: each
                // role:"subflow" module serializes back into a
                // type:"subflow" definition node (subflow_module_tree).
                this.serializeSubflowDefs().forEach(d => flow.nodes.push(d));
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
             * modules. Checkpoint nodes are intentionally excluded —
             * problemQueueIDFromView rejects them (an approval gate is
             * not a dead-letter queue). Canvas ids ARE the persisted
             * backend node ids (exportFlow writes them through).
             */
            queueNodeOptions() {
                const df = (this.editor && this.editor.drawflow.drawflow) || {};
                const out = [];
                Object.keys(df).forEach(modName => {
                    const meta = (typeof this.editor.getModuleMeta === 'function')
                        ? (this.editor.getModuleMeta(modName) || {}) : {};
                    if ((meta.role || 'flow') === 'subflow') return;
                    const nodes = df[modName].data || {};
                    Object.keys(nodes).forEach(id => {
                        if (nodes[id].class !== 'queue') return;
                        const label = (nodes[id].data && nodes[id].data.name) || String(id);
                        out.push({ id: String(id), label: label + ' — ' + (meta.label || modName) });
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
             * adds a subflow definition to that workflow — it selects
             * the workflow first so createSubflow edits the loaded
             * document (same pattern as openWorkflowPropsFor).
             */
            async addSubflowToWorkflow(workflow) {
                if (!workflow) return;
                if (!this.selectedWorkflow || this.selectedWorkflow.id !== workflow.id) {
                    await this.selectWorkflow(workflow);
                }
                this.createSubflow();
            },

            /**
             * Commit the modal: name/description land on the workflow
             * document (sent by saveWorkflow), the view fields merge
             * into selectedWorkflow.view — set keys overwrite, empty
             * fields DELETE the key so the engine default applies, and
             * unrelated keys (openModules, env, disabledModules,
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
             * Bounding box of all nodes in the current module
             * (approximate node size 200x90)
             */
            nodeBounds() {
                const mod = this.editor.drawflow.drawflow[this.editor.module];
                const data = (mod && mod.data) || {};
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
             * Zoom + pan so all nodes in the current module fit the viewport
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
                const mod = this.editor.drawflow.drawflow[this.editor.module];
                const data = (mod && mod.data) || {};
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
             * Serialized snapshot of the whole editor (all modules);
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

            /* ---- Flow tabs + module navigator (task 11 / modules_as_first_class) ---- */

            /**
             * Switch the visible canvas to another module/tab
             */
            switchTab(tab) {
                if (this.editor.module !== tab.module) {
                    this.editor.changeModule(tab.module);
                }
                this.activeTab = tab.module;
                this.scheduleMinimap();
            },

            /**
             * Create a new empty module and open it in a tab (used by
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
                const tab = { module: name, label: name, role: 'flow' };
                this.flowTabs.push(tab);
                this.knownModules.push(name);
                this.moduleTreeVersion++;
                this.switchTab(tab);
                this.pushHistory();
                this.dirty = true;
                return name;
            },

            /**
             * Close a tab — non-destructive. The module stays
             * materialized in Drawflow and listed in the tree; only the
             * tab goes away. The open set persists via view.openModules,
             * so the close survives save + reload. Deleting a module is
             * a separate tree action (deleteModuleByName).
             */
            closeFlowTab(tab) {
                if (this.editor.module === tab.module) {
                    const idx = this.flowTabs.findIndex(t => t.module === tab.module);
                    const next = this.flowTabs[idx === 0 ? 1 : idx - 1];
                    // Closing the last tab empties the workspace — the
                    // welcome screen takes over.
                    if (next && next !== tab) {
                        this.switchTab(next);
                    } else {
                        this.activeTab = null;
                    }
                }
                this.flowTabs = this.flowTabs.filter(t => t.module !== tab.module);
                this.moduleTreeVersion++;
                this.dirty = true;
            },

            /* ---- Module navigator tree ---- */

            /**
             * Open a module from the tree: create its tab if absent,
             * then activate it. Already-open items just activate.
             */
            openModuleItem(item) {
                let tab = this.flowTabs.find(t => t.module === item.module);
                if (!tab) {
                    tab = { module: item.module, label: item.label, role: item.role };
                    this.flowTabs.push(tab);
                    // The open set persists via view.openModules
                    this.dirty = true;
                    this.moduleTreeVersion++;
                }
                this.switchTab(tab);
            },

            /**
             * Right-click on a tree item: open the module context menu.
             */
            onModuleContextMenu(e, item) {
                e.preventDefault();
                e.stopPropagation();
                this.moduleContextMenu = {
                    show: true,
                    x: e.clientX,
                    y: e.clientY,
                    module: item.module
                };
            },

            closeModuleContextMenu() {
                this.moduleContextMenu.show = false;
            },

            /* ---- Sidebar file tree (sidebar_file_tree) ---- */

            /**
             * Sanitized folder list persisted as view.treeGroups:
             * [{id,name,parentId,modules:[moduleId]}]. Drops parent
             * ids pointing at deleted folders and module refs that no
             * longer resolve to a materialized module; a module id
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
                    modules: (g.modules || [])
                        .map(mid => this.resolveModuleName(String(mid)))
                        .filter(name => !!df[name])
                        .map(name => this.moduleIdFor(name))
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
                    modules: []
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
             * only — member modules are never touched and return to
             * the ungrouped root list.
             */
            deleteTreeFolder(folder) {
                if (!folder) return;
                if (!confirm(`Delete folder "${folder.name}"? Modules inside move back to the root — nothing else is removed.`)) return;
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
             * Move a module into a folder (null = ungrouped root).
             * Membership is exclusive: the module id leaves every
             * other folder's list. Stored by module id so renames do
             * not break membership.
             */
            moveModuleToFolder(moduleKey, folderId) {
                const mid = String(this.moduleIdFor(moduleKey));
                this.treeGroups.forEach(g => {
                    g.modules = (g.modules || []).filter(m => String(m) !== mid);
                });
                const folder = folderId ? this.folderById(folderId) : null;
                if (folder) {
                    if (!Array.isArray(folder.modules)) folder.modules = [];
                    folder.modules.push(mid);
                }
                this.dirty = true;
            },

            /** Create a module and place it inside a folder. */
            createModuleInFolder(role, folderId) {
                const key = role === 'subflow' ? this.createSubflow() : this.addFlowTab();
                if (key) this.moveModuleToFolder(key, folderId);
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
                this.openModuleItem(row.item);
            },

            onTreeRowDblClick(row) {
                if (row.type === 'workflow') this.openWorkflowPropsFor(row.workflow);
            },

            onTreeRowContextMenu(e, row) {
                if (row.type === 'module') {
                    this.onModuleContextMenu(e, row.item);
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
             * Sidebar affordance: module-folder creation lives on the
             * workflow row's context menu — the folder list is per
             * workflow document, so select first like addSubflowToWorkflow.
             */
            async createTreeFolderFor(workflow) {
                if (!workflow) return;
                if (!this.selectedWorkflow || this.selectedWorkflow.id !== workflow.id) {
                    await this.selectWorkflow(workflow);
                }
                this.createTreeFolder(null);
            },

            /* Module leaf drag-and-drop into folders (or back to the
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
                if (row.type !== 'module') {
                    e.preventDefault();
                    return;
                }
                this.dragModuleKey = row.item.module;
                try {
                    e.dataTransfer.setData('text/x-flow-module', row.item.module);
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
                if (!this.dragModuleKey) return;
                const ok = row.type === 'folder' ||
                    (row.type === 'workflow' && row.selected) ||
                    (row.type === 'module');
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
                const key = this.dragModuleKey;
                this.dragModuleKey = null;
                this.treeDropKey = null;
                if (!key) return;
                e.preventDefault();
                if (row.type === 'folder') {
                    this.moveModuleToFolder(key, row.folder.id);
                } else if (row.type === 'workflow' && row.selected) {
                    this.moveModuleToFolder(key, null);
                } else if (row.type === 'module') {
                    // Dropping onto a leaf files it next to that leaf:
                    // the leaf's folder (or root when ungrouped).
                    const folder = this.treeGroups.find(g =>
                        (g.modules || []).some(mid =>
                            this.resolveModuleName(String(mid)) === row.item.module));
                    this.moveModuleToFolder(key, folder ? folder.id : null);
                }
            },

            /* ---- Module export/import (module_export_import) ---- */

            /**
             * Export one module as a portable module envelope
             * ({nodestore:"module", version:1}) and copy it to the
             * clipboard. Saves first when the canvas is dirty so the
             * export reflects the visible state. External references
             * the export kept but could not resolve come back as
             * warnings.
             */
            async exportModule(moduleKey) {
                if (!this.selectedWorkflow) return;
                // A role:"subflow" module is a definition — its portable
                // form is the def envelope (ExportSubflowDef), not the
                // node-membership module export.
                if (this.moduleRoleFor(moduleKey) === 'subflow') {
                    return this.exportSubflowDef({ id: this.moduleIdFor(moduleKey) });
                }
                try {
                    if (this.dirty) await this.saveWorkflow();
                    const data = await this.api('/api/module-export', {
                        id: this.selectedWorkflow.id,
                        module: this.moduleIdFor(moduleKey)
                    });
                    const json = JSON.stringify(data.module_export, null, 2);
                    const warnCount = (data.warnings || []).length;
                    (data.warnings || []).forEach(w => console.warn('module export:', w));
                    const note = warnCount ? ' — ' + warnCount + ' external-ref warning(s), see console' : '';
                    if (navigator.clipboard && navigator.clipboard.writeText) {
                        navigator.clipboard.writeText(json)
                            .then(() => this.showToast('Module export copied to clipboard' + note, warnCount ? 'warning' : 'info'))
                            .catch(() => this.fallbackCopyJson(json));
                    } else {
                        this.fallbackCopyJson(json);
                        if (warnCount) this.showToast('Module exported with ' + warnCount + ' warning(s)', 'warning');
                    }
                } catch (err) {
                    this.showToast('Module export failed: ' + err.message, 'danger');
                }
            },

            /**
             * Export a single subflow definition (the "Subflow
             * Definitions" list action): the same envelope, carrying
             * the def plus any defs it nests.
             */
            async exportSubflowDef(def) {
                if (!this.selectedWorkflow || !def) return;
                try {
                    if (this.dirty) await this.saveWorkflow();
                    const data = await this.api('/api/module-export', {
                        id: this.selectedWorkflow.id,
                        subflow: def.id
                    });
                    const json = JSON.stringify(data.module_export, null, 2);
                    if (navigator.clipboard && navigator.clipboard.writeText) {
                        navigator.clipboard.writeText(json)
                            .then(() => this.showToast('Subflow definition export copied to clipboard', 'info'))
                            .catch(() => this.fallbackCopyJson(json));
                    } else {
                        this.fallbackCopyJson(json);
                    }
                } catch (err) {
                    this.showToast('Subflow export failed: ' + err.message, 'danger');
                }
            },

            /**
             * Open the "Import module" modal (tree group-header action):
             * pasting a module envelope merges it into THIS workflow as
             * a new module — unlike the toolbar Import, which replaces
             * the canvas or creates a whole new workflow.
             */
            openModuleImport() {
                if (!this.selectedWorkflow) {
                    this.showToast('Select a workflow first', 'info');
                    return;
                }
                this.moduleImportJson = '';
                this.moduleImportError = '';
                bootstrap.Modal.getOrCreateInstance(this.$refs.moduleImportModal).show();
            },

            /**
             * Merge the pasted module envelope into the selected
             * workflow via the backend (fresh module + node ids,
             * deduped label), then re-render the canvas.
             */
            async confirmModuleImport() {
                let parsed;
                try {
                    parsed = JSON.parse(this.moduleImportJson);
                } catch (err) {
                    this.moduleImportError = 'Invalid JSON: ' + err.message;
                    return;
                }
                try {
                    // The backend merges into the stored document, so
                    // unsaved canvas edits must be persisted first.
                    if (this.dirty) await this.saveWorkflow();
                    const data = await this.api('/api/module-import', {
                        id: this.selectedWorkflow.id,
                        module_export: parsed
                    });
                    bootstrap.Modal.getInstance(this.$refs.moduleImportModal).hide();
                    if (data.workflow) {
                        this.selectedWorkflow = data.workflow;
                        this.renderFlow(data.workflow);
                        this.applyView(data.workflow.view);
                        this.dirty = false;
                    }
                    const warnings = data.warnings || [];
                    warnings.forEach(w => console.warn('module import:', w));
                    this.showToast('Module imported' +
                        (warnings.length ? ' — ' + warnings.length + ' warning(s), see console' : ''),
                        warnings.length ? 'warning' : 'success');
                } catch (err) {
                    this.moduleImportError = 'Import failed: ' + err.message;
                }
            },

            /**
             * Rename a module (tab or tree driven). The rename moves the
             * module object (meta included), so the stable meta.id — the
             * value nodes carry in data.module — is untouched; only
             * meta.label follows the new name. No node rewrites needed.
             * Returns the new canvas key on success, null otherwise.
             */
            renameModuleByName(moduleKey) {
                const current = this.moduleLabelFor(moduleKey);
                const name = (window.prompt('Rename module', current) || '').trim();
                if (!name || name === moduleKey) return null;
                if (this.editor.drawflow.drawflow[name]) {
                    this.showToast(`A module named "${name}" already exists`, 'error');
                    return null;
                }
                if (typeof this.editor.renameModule !== 'function' || !this.editor.renameModule(moduleKey, name)) {
                    this.showToast('Rename failed', 'error');
                    return null;
                }
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(name, { label: name });
                }
                const tab = this.flowTabs.find(t => t.module === moduleKey);
                const wasActive = this.editor.module === moduleKey || this.activeTab === moduleKey;
                if (tab) {
                    tab.module = name;
                    tab.label = name;
                    if (wasActive) this.switchTab(tab);
                } else if (wasActive) {
                    this.editor.changeModule(name);
                    this.activeTab = name;
                }
                this.knownModules = this.knownModules.map(m => (m === moduleKey ? name : m));
                this.moduleTreeVersion++;
                this.pushHistory();
                this.dirty = true;
                return name;
            },

            renameFlowTab(tab) {
                this.renameModuleByName(tab.module);
            },

            /**
             * Enable/disable a module from the tree context menu.
             * The flag lives on meta.enabled (persisted via view.modules
             * and mirrored into view.disabledModules on save); an open
             * tab's disabled flag follows it.
             */
            toggleModuleDisabled(moduleKey) {
                const meta = (this.editor && typeof this.editor.getModuleMeta === 'function')
                    ? this.editor.getModuleMeta(moduleKey) : null;
                const disabled = !(meta && meta.enabled === false); // now disabled?
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(moduleKey, { enabled: !disabled });
                }
                const tab = this.flowTabs.find(t => t.module === moduleKey);
                if (tab) tab.disabled = disabled;
                this.refreshDisabledDom();
                this.moduleTreeVersion++;
                this.dirty = true;
                this.pushHistory();
            },

            /**
             * Delete a module entirely — the only destructive module
             * action. Removes its nodes/edges from storage (the canvas
             * module is the export source), closes its tab if open.
             * The last module cannot be deleted.
             */
            deleteModuleByName(moduleKey) {
                const df = this.editor.drawflow.drawflow || {};
                const names = Object.keys(df);
                if (names.length <= 1) {
                    this.showToast('Cannot delete the last module', 'info');
                    return;
                }
                const label = this.moduleLabelFor(moduleKey);
                if (!confirm(`Delete module "${label}"? Its nodes and wires will be removed.`)) return;

                // If the doomed module is the visible one, activate a
                // survivor first — removeModule on the current module
                // leaves Drawflow pointed at a dead key.
                if (this.editor.module === moduleKey) {
                    const survivor = names.find(m => m !== moduleKey);
                    let survivorTab = this.flowTabs.find(t => t.module === survivor);
                    if (!survivorTab) {
                        survivorTab = {
                            module: survivor,
                            label: this.moduleLabelFor(survivor),
                            role: this.moduleRoleFor(survivor)
                        };
                        this.flowTabs.push(survivorTab);
                    }
                    this.editor.changeModule(survivor);
                    this.activeTab = survivor;
                }
                this.flowTabs = this.flowTabs.filter(t => t.module !== moduleKey);
                try {
                    this.editor.removeModule(moduleKey);
                } catch (err) {
                    console.warn('Failed to delete module:', err);
                }
                this.knownModules = this.knownModules.filter(m => m !== moduleKey);
                this.moduleTreeVersion++;
                this.refreshDisabledDom();
                this.pushHistory();
                this.dirty = true;
            },

            /**
             * Toggle a whole flow tab disabled (Node-RED "disabled":
             * true on the tab object). Persists as view.disabledModules;
             * every node of the module deploys inert.
             */
            toggleFlowTabDisabled(tab) {
                tab.disabled = !tab.disabled;
                // Keep the module entity's enabled flag in sync so
                // view.modules reflects the same state as
                // view.disabledModules.
                if (typeof this.editor.setModuleMeta === 'function') {
                    this.editor.setModuleMeta(tab.module, { enabled: !tab.disabled });
                }
                this.refreshDisabledDom();
                this.moduleTreeVersion++;
                this.dirty = true;
                this.pushHistory();
            },

            /**
             * Rebuild the open-tab list after an import / undo snapshot
             * restore: keep tabs whose module still exists and open any
             * module that appeared since the last sync (a restored or
             * freshly imported module). Modules that were closed before
             * stay closed — the tree still lists them.
             */
            syncTabsFromEditor() {
                const modules = Object.keys((this.editor.drawflow && this.editor.drawflow.drawflow) || {});
                const prev = this.knownModules || [];
                const appeared = modules.filter(m => prev.indexOf(m) === -1);
                this.flowTabs = modules
                    .filter(m => this.flowTabs.some(t => t.module === m) || appeared.indexOf(m) !== -1)
                    .map(m => {
                        const tab = this.flowTabs.find(t => t.module === m);
                        return {
                            module: m,
                            label: tab ? tab.label : this.moduleLabelFor(m),
                            role: this.moduleRoleFor(m)
                        };
                    });
                this.knownModules = modules.slice();
                this.activeTab = this.flowTabs.length ? this.editor.module : null;
                this.moduleTreeVersion++;
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
