// Settings page Vue app. Mounted by the Go-composed page
// (settings_page.go); AJAX URLs are injected as
// window.settingsManagerUrls — no hardcoded API paths here.
const { createApp } = Vue;
const urls = window.settingsManagerUrls || {};

const app = createApp({
    data() {
        return {
            loading: true,
            saving: false,
            engine: {},
            startup: {},
            server: {},
            overrides: {},
            envRows: [],
            // Form mirrors the override set: '' means "no override —
            // inherit the startup (env-var) value".
            form: { maxSteps: '', allowExec: '', fileRoot: '' }
        };
    },
    computed: {
        hasOverrides() {
            return this.overrides.maxSteps !== undefined ||
                   this.overrides.allowExec !== undefined ||
                   this.overrides.fileRoot !== undefined;
        }
    },
    mounted() {
        this.load();
    },
    methods: {
        /**
         * POST to a JSON endpoint and unwrap the {status, data, message}
         * envelope (same helper shape as the editor).
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

        async load() {
            this.loading = true;
            try {
                const data = await this.api(urls.loadSettings);
                this.engine = data.engine || {};
                this.startup = data.startup || {};
                this.server = data.server || {};
                this.overrides = data.overrides || {};
                this.form = {
                    maxSteps: this.overrides.maxSteps !== undefined ? String(this.overrides.maxSteps) : '',
                    allowExec: this.overrides.allowExec === undefined ? '' : (this.overrides.allowExec ? 'enabled' : 'disabled'),
                    fileRoot: this.overrides.fileRoot !== undefined ? this.overrides.fileRoot : ''
                };
            } catch (err) {
                Notiflix.Notify.failure('Failed to load settings: ' + err.message);
            }
            try {
                const env = await this.api(urls.globalEnvFetch);
                this.envRows = Array.isArray(env.env) ? env.env : [];
            } catch (err) {
                this.envRows = [];
            }
            this.loading = false;
        },

        async save() {
            this.saving = true;
            const payload = {};
            if (this.form.maxSteps !== '') {
                const n = Number(this.form.maxSteps);
                if (!isFinite(n)) {
                    Notiflix.Notify.warning('Max steps must be a number');
                    this.saving = false;
                    return;
                }
                payload.maxSteps = Math.trunc(n);
            }
            if (this.form.allowExec === 'enabled') payload.allowExec = true;
            if (this.form.allowExec === 'disabled') payload.allowExec = false;
            if (this.form.fileRoot !== '') payload.fileRoot = this.form.fileRoot;
            try {
                await this.api(urls.saveSetting, payload);
                Notiflix.Notify.success('Settings saved.');
                await this.load();
            } catch (err) {
                Notiflix.Notify.failure('Save failed: ' + err.message);
            }
            this.saving = false;
        }
    }
});
app.mount('#app');
