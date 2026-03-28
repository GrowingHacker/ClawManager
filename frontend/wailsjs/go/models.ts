export namespace dashboard {
	
	export class DashboardStatus {
	    running: boolean;
	    port: number;
	    url: string;
	    fullUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new DashboardStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.port = source["port"];
	        this.url = source["url"];
	        this.fullUrl = source["fullUrl"];
	    }
	}

}

export namespace plugins {
	
	export class Plugin {
	    id: string;
	    name: string;
	    version: string;
	    status: string;
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Plugin(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.version = source["version"];
	        this.status = source["status"];
	        this.enabled = source["enabled"];
	    }
	}

}

export namespace wsl {
	
	export class OpenClawStatus {
	    installed: boolean;
	    version: string;
	    gatewayRunning: boolean;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new OpenClawStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.version = source["version"];
	        this.gatewayRunning = source["gatewayRunning"];
	        this.error = source["error"];
	    }
	}
	export class WSLInfo {
	    installed: boolean;
	    distroInstalled: boolean;
	    version: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new WSLInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.distroInstalled = source["distroInstalled"];
	        this.version = source["version"];
	        this.error = source["error"];
	    }
	}

}

