export namespace config {
	
	export class SlotBinding {
	    slot: number;
	    deviceId: string;
	    name: string;
	    model: string;
	    ip: string;
	
	    static createFrom(source: any = {}) {
	        return new SlotBinding(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slot = source["slot"];
	        this.deviceId = source["deviceId"];
	        this.name = source["name"];
	        this.model = source["model"];
	        this.ip = source["ip"];
	    }
	}

}

export namespace govee {
	
	export class Device {
	    id: string;
	    name: string;
	    model: string;
	    ip: string;
	    online: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Device(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.model = source["model"];
	        this.ip = source["ip"];
	        this.online = source["online"];
	    }
	}

}

export namespace main {
	
	export class SettingsView {
	    midiCC: number;
	    midiCCAlt: number;
	    midiNotePlus: number;
	    midiNoteMinus: number;
	    idleHideSeconds: number;
	    hasEnvKey: boolean;
	    hasConfigKey: boolean;
	    hasApiKey: boolean;
	    envPath: string;
	    configPath: string;
	    mappingPath: string;
	
	    static createFrom(source: any = {}) {
	        return new SettingsView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.midiCC = source["midiCC"];
	        this.midiCCAlt = source["midiCCAlt"];
	        this.midiNotePlus = source["midiNotePlus"];
	        this.midiNoteMinus = source["midiNoteMinus"];
	        this.idleHideSeconds = source["idleHideSeconds"];
	        this.hasEnvKey = source["hasEnvKey"];
	        this.hasConfigKey = source["hasConfigKey"];
	        this.hasApiKey = source["hasApiKey"];
	        this.envPath = source["envPath"];
	        this.configPath = source["configPath"];
	        this.mappingPath = source["mappingPath"];
	    }
	}
	export class SlotView {
	    number: number;
	    deviceId: string;
	    name: string;
	    model: string;
	    ip: string;
	    active: boolean;
	    online: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SlotView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.number = source["number"];
	        this.deviceId = source["deviceId"];
	        this.name = source["name"];
	        this.model = source["model"];
	        this.ip = source["ip"];
	        this.active = source["active"];
	        this.online = source["online"];
	    }
	}
	export class HUDState {
	    slots: SlotView[];
	    activePool: number[];
	    brightness: number;
	    paletteIndex: number;
	    paletteName: string;
	    midiConnected: boolean;
	    midiPort: string;
	    deviceCount: number;
	    needsSetup: boolean;
	    setupOpen: boolean;
	    hasApiKey: boolean;
	    discoverError: string;
	    discovering: boolean;
	    firstRun: boolean;
	    catalog: govee.Device[];
	    hidden: boolean;
	    mappingPath: string;
	    configOpen: boolean;
	    dancing: boolean;
	    settings: SettingsView;
	
	    static createFrom(source: any = {}) {
	        return new HUDState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.slots = this.convertValues(source["slots"], SlotView);
	        this.activePool = source["activePool"];
	        this.brightness = source["brightness"];
	        this.paletteIndex = source["paletteIndex"];
	        this.paletteName = source["paletteName"];
	        this.midiConnected = source["midiConnected"];
	        this.midiPort = source["midiPort"];
	        this.deviceCount = source["deviceCount"];
	        this.needsSetup = source["needsSetup"];
	        this.setupOpen = source["setupOpen"];
	        this.hasApiKey = source["hasApiKey"];
	        this.discoverError = source["discoverError"];
	        this.discovering = source["discovering"];
	        this.firstRun = source["firstRun"];
	        this.catalog = this.convertValues(source["catalog"], govee.Device);
	        this.hidden = source["hidden"];
	        this.mappingPath = source["mappingPath"];
	        this.configOpen = source["configOpen"];
	        this.dancing = source["dancing"];
	        this.settings = this.convertValues(source["settings"], SettingsView);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	

}

