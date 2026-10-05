export namespace localfiles {
	
	export class Entry {
	    name: string;
	    path: string;
	    isDir: boolean;
	    isSymlink: boolean;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	        this.isSymlink = source["isSymlink"];
	        this.size = source["size"];
	    }
	}
	export class Directory {
	    path: string;
	    parentPath: string;
	    homePath: string;
	    entries: Entry[];
	
	    static createFrom(source: any = {}) {
	        return new Directory(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.parentPath = source["parentPath"];
	        this.homePath = source["homePath"];
	        this.entries = this.convertValues(source["entries"], Entry);
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

export namespace main {
	
	export class UploadSelection {
	    native: boolean;
	    entries: localfiles.Entry[];
	
	    static createFrom(source: any = {}) {
	        return new UploadSelection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.native = source["native"];
	        this.entries = this.convertValues(source["entries"], localfiles.Entry);
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

export namespace model {
	
	export class AppInfo {
	    version: string;
	    dataDir: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.dataDir = source["dataDir"];
	    }
	}
	export class Connection {
	    id: string;
	    profileId: number;
	    name: string;
	    home: string;
	
	    static createFrom(source: any = {}) {
	        return new Connection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.profileId = source["profileId"];
	        this.name = source["name"];
	        this.home = source["home"];
	    }
	}
	export class RemoteEntry {
	    name: string;
	    path: string;
	    isDir: boolean;
	    isSymlink: boolean;
	    size: number;
	    modifiedAt: string;
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new RemoteEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	        this.isSymlink = source["isSymlink"];
	        this.size = source["size"];
	        this.modifiedAt = source["modifiedAt"];
	        this.mode = source["mode"];
	    }
	}
	export class Directory {
	    path: string;
	    entries: RemoteEntry[];
	
	    static createFrom(source: any = {}) {
	        return new Directory(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.entries = this.convertValues(source["entries"], RemoteEntry);
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
	export class Profile {
	    id: number;
	    name: string;
	    host: string;
	    port: number;
	    username: string;
	    authKind: string;
	    keyPath: string;
	    groupName: string;
	    remark: string;
	    lastConnectedAt: string;
	    osId: string;
	    cpuCores: number;
	    memoryBytes: number;
	    diskBytes: number;
	    hasSecret: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Profile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.host = source["host"];
	        this.port = source["port"];
	        this.username = source["username"];
	        this.authKind = source["authKind"];
	        this.keyPath = source["keyPath"];
	        this.groupName = source["groupName"];
	        this.remark = source["remark"];
	        this.lastConnectedAt = source["lastConnectedAt"];
	        this.osId = source["osId"];
	        this.cpuCores = source["cpuCores"];
	        this.memoryBytes = source["memoryBytes"];
	        this.diskBytes = source["diskBytes"];
	        this.hasSecret = source["hasSecret"];
	    }
	}

}

