// CoreBluetooth bridge for Govee BLE lamps.
//
// Everything runs on a private serial dispatch queue — the CBCentralManager
// delegate queue — and never touches the main thread. (The off-the-shelf Go
// BLE bindings pump a nested run loop on the main queue, which corrupts the
// AppKit loop Wails owns and crashes the app.)
//
// H6001 (and other classic Govee bulbs) ignore writes to 2b11 until
// notifications are enabled on 2b10. Discover both, subscribe, then write.

#import <Foundation/Foundation.h>
#import <CoreBluetooth/CoreBluetooth.h>
#include "_cgo_export.h"

@interface LWBle : NSObject <CBCentralManagerDelegate, CBPeripheralDelegate>
@property(strong) CBCentralManager *cm;
@property(strong) dispatch_queue_t q;
@property(strong) NSMutableDictionary<NSString *, CBPeripheral *> *prphs;
@property(strong) NSMutableDictionary<NSString *, CBCharacteristic *> *chars;
@property(strong) NSMutableSet<NSString *> *fullDiscover;
@end

static LWBle *gBle = nil;
static CBUUID *gSvc = nil;
static CBUUID *gChr = nil;
static CBUUID *gNtf = nil;

static NSString *lwKey(NSString *uuid) {
    return uuid.uppercaseString;
}

static void lwMsg(NSString *s) {
    if (s == nil) {
        return;
    }
    NSLog(@"govee ble: %@", s);
    goBLEMsg((char *)s.UTF8String);
}

static NSString *lwHex(NSData *data) {
    const unsigned char *b = data.bytes;
    NSMutableString *hex = [NSMutableString string];
    NSUInteger n = data.length < 20 ? data.length : 20;
    for (NSUInteger i = 0; i < n; i++) {
        [hex appendFormat:@"%02x", b[i]];
        if (i + 1 < n) {
            [hex appendString:@" "];
        }
    }
    return hex;
}

// H6001 (and kin) often omit the GAP name on the first advertisement and only
// carry Govee manufacturer 0xEC88 / service 1910. A name-only filter drops them.
static BOOL lwLooksGovee(NSString *name, NSDictionary *ad) {
    if (name.length > 0) {
        NSString *u = name.uppercaseString;
        if ([u containsString:@"IHOMENT"] || [u containsString:@"GOVEE"] ||
            [u containsString:@"GVH_"] || [u containsString:@"GBK_"] ||
            [u containsString:@"MINGER"] || [u containsString:@"IHOM_"] ||
            [u containsString:@"H60"] || [u containsString:@"H61"] ||
            [u containsString:@"H70"] || [u containsString:@"H80"]) {
            return YES;
        }
    }
    NSMutableArray *svcs = [NSMutableArray array];
    NSArray *listed = ad[CBAdvertisementDataServiceUUIDsKey];
    if (listed.count) {
        [svcs addObjectsFromArray:listed];
    }
    NSArray *overflow = ad[CBAdvertisementDataOverflowServiceUUIDsKey];
    if (overflow.count) {
        [svcs addObjectsFromArray:overflow];
    }
    for (CBUUID *u in svcs) {
        if ([u isEqual:gSvc]) {
            return YES;
        }
        NSString *s = u.UUIDString.uppercaseString;
        if ([s hasSuffix:@"1910"] || [s containsString:@"0A0B0C0D1910"]) {
            return YES;
        }
    }
    NSData *mfr = ad[CBAdvertisementDataManufacturerDataKey];
    if (mfr.length >= 2) {
        const unsigned char *b = mfr.bytes;
        // Bluetooth company IDs are little-endian. Govee is 0xEC88.
        if (b[0] == 0x88 && b[1] == 0xEC) {
            return YES;
        }
    }
    return NO;
}

static void lwHarvestConnected(void) {
    if (gBle == nil || gBle.cm.state != CBManagerStatePoweredOn) {
        return;
    }
    NSArray *connected = [gBle.cm retrieveConnectedPeripheralsWithServices:@[ gSvc ]];
    lwMsg([NSString stringWithFormat:@"harvest connected=%lu", (unsigned long)connected.count]);
    for (CBPeripheral *cp in connected) {
        NSString *uuid = lwKey(cp.identifier.UUIDString);
        gBle.prphs[uuid] = cp;
        NSString *name = cp.name.length ? cp.name : @"Govee BLE";
        lwMsg([NSString stringWithFormat:@"connected peripheral %@ %@", name, uuid]);
        goBLEFound((char *)uuid.UTF8String, (char *)name.UTF8String);
    }
}

@implementation LWBle

- (instancetype)init {
    self = [super init];
    if (self) {
        _q = dispatch_queue_create("lightwave.ble", DISPATCH_QUEUE_SERIAL);
        _prphs = [NSMutableDictionary new];
        _chars = [NSMutableDictionary new];
        _fullDiscover = [NSMutableSet new];
        NSDictionary *opts = @{CBCentralManagerOptionShowPowerAlertKey : @YES};
        _cm = [[CBCentralManager alloc] initWithDelegate:self queue:_q options:opts];
    }
    return self;
}

- (void)centralManagerDidUpdateState:(CBCentralManager *)central {
    goBLEState((int)central.state);
}

- (void)centralManager:(CBCentralManager *)central
    didDiscoverPeripheral:(CBPeripheral *)p
        advertisementData:(NSDictionary *)ad
                     RSSI:(NSNumber *)rssi {
    NSString *name = ad[CBAdvertisementDataLocalNameKey] ?: p.name;
    if (!lwLooksGovee(name, ad)) {
        if (name.length && [name.uppercaseString containsString:@"H60"]) {
            lwMsg([NSString stringWithFormat:@"skip %@", name]);
        }
        return;
    }
    if (name.length == 0) {
        name = p.name.length ? p.name : @"Govee BLE";
    }
    NSString *uuid = lwKey(p.identifier.UUIDString);
    self.prphs[uuid] = p;
    goBLEFound((char *)uuid.UTF8String, (char *)name.UTF8String);
}

- (void)centralManager:(CBCentralManager *)central didConnectPeripheral:(CBPeripheral *)p {
    NSString *uuid = lwKey(p.identifier.UUIDString);
    lwMsg([NSString stringWithFormat:@"connected %@", uuid]);
    p.delegate = self;
    [p discoverServices:@[ gSvc ]];
}

- (void)centralManager:(CBCentralManager *)central
    didFailToConnectPeripheral:(CBPeripheral *)p
                         error:(NSError *)e {
    NSString *uuid = lwKey(p.identifier.UUIDString);
    lwMsg([NSString stringWithFormat:@"connect failed %@: %@", uuid, e]);
    goBLEGone((char *)uuid.UTF8String);
}

- (void)centralManager:(CBCentralManager *)central
    didDisconnectPeripheral:(CBPeripheral *)p
                      error:(NSError *)e {
    NSString *uuid = lwKey(p.identifier.UUIDString);
    [self.chars removeObjectForKey:uuid];
    [self.fullDiscover removeObject:uuid];
    lwMsg([NSString stringWithFormat:@"disconnected %@ err=%@", uuid, e]);
    goBLEGone((char *)uuid.UTF8String);
}

- (void)peripheral:(CBPeripheral *)p didDiscoverServices:(NSError *)e {
    NSString *uuid = lwKey(p.identifier.UUIDString);
    if (e != nil) {
        lwMsg([NSString stringWithFormat:@"services error %@: %@", uuid, e]);
        [self.cm cancelPeripheralConnection:p];
        return;
    }
    for (CBService *s in p.services) {
        if ([s.UUID isEqual:gSvc]) {
            [p discoverCharacteristics:@[ gNtf, gChr ] forService:s];
            return;
        }
    }
    lwMsg([NSString stringWithFormat:@"govee service 1910 missing %@", uuid]);
    [self.cm cancelPeripheralConnection:p];
}

- (void)peripheral:(CBPeripheral *)p
    didDiscoverCharacteristicsForService:(CBService *)s
                                   error:(NSError *)e {
    NSString *uuid = lwKey(p.identifier.UUIDString);
    if (e != nil) {
        lwMsg([NSString stringWithFormat:@"chars error %@: %@", uuid, e]);
        [self.cm cancelPeripheralConnection:p];
        return;
    }
    BOOL haveWrite = NO;
    BOOL haveNtf = NO;
    for (CBCharacteristic *c in s.characteristics) {
        if ([c.UUID isEqual:gChr]) {
            self.chars[uuid] = c;
            haveWrite = YES;
            lwMsg([NSString stringWithFormat:@"write char 2b11 %@ props=0x%lx", uuid,
                                             (unsigned long)c.properties]);
        }
        if ([c.UUID isEqual:gNtf]) {
            haveNtf = YES;
            lwMsg([NSString stringWithFormat:@"notify char 2b10 %@ — enabling", uuid]);
            [p setNotifyValue:YES forCharacteristic:c];
        }
    }
    if (!haveWrite) {
        if (![self.fullDiscover containsObject:uuid]) {
            [self.fullDiscover addObject:uuid];
            lwMsg([NSString stringWithFormat:@"2b11 missing, discovering all chars %@", uuid]);
            [p discoverCharacteristics:nil forService:s];
            return;
        }
        lwMsg([NSString stringWithFormat:@"write characteristic 2b11 missing %@", uuid]);
        [self.cm cancelPeripheralConnection:p];
        return;
    }
    if (!haveNtf) {
        // Some SKUs only expose 2b11. Still mark ready so writes can proceed.
        goBLEReady((char *)uuid.UTF8String);
        return;
    }
    // Notify enable is asynchronous. Mark ready once it lands; also fall
    // through after a short wait so a silent notify callback cannot stall us.
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 300 * NSEC_PER_MSEC), self.q, ^{
        if (self.chars[uuid] != nil) {
            goBLEReady((char *)uuid.UTF8String);
        }
    });
}

- (void)peripheral:(CBPeripheral *)p
    didUpdateNotificationStateForCharacteristic:(CBCharacteristic *)c
                                          error:(NSError *)e {
    NSString *uuid = lwKey(p.identifier.UUIDString);
    if (e != nil) {
        lwMsg([NSString stringWithFormat:@"notify error %@: %@", uuid, e]);
    } else {
        lwMsg([NSString stringWithFormat:@"notify %@ %@", uuid, c.isNotifying ? @"on" : @"off"]);
    }
    if (self.chars[uuid] != nil) {
        goBLEReady((char *)uuid.UTF8String);
    }
}

- (void)peripheral:(CBPeripheral *)p
    didUpdateValueForCharacteristic:(CBCharacteristic *)c
                              error:(NSError *)e {
    if (e != nil || c.value == nil) {
        return;
    }
    lwMsg([NSString stringWithFormat:@"notify rx %@: %@", lwKey(p.identifier.UUIDString), lwHex(c.value)]);
}

- (void)peripheral:(CBPeripheral *)p
    didWriteValueForCharacteristic:(CBCharacteristic *)c
                             error:(NSError *)e {
    NSString *uuid = lwKey(p.identifier.UUIDString);
    if (e != nil) {
        lwMsg([NSString stringWithFormat:@"write error %@: %@", uuid, e]);
        goBLEWriteFail((char *)uuid.UTF8String);
    }
}

@end

void lw_ble_init(void) {
    if (gBle != nil) {
        return;
    }
    gSvc = [CBUUID UUIDWithString:@"00010203-0405-0607-0809-0A0B0C0D1910"];
    gNtf = [CBUUID UUIDWithString:@"00010203-0405-0607-0809-0A0B0C0D2B10"];
    gChr = [CBUUID UUIDWithString:@"00010203-0405-0607-0809-0A0B0C0D2B11"];
    gBle = [[LWBle alloc] init];
}

void lw_ble_scan(int on) {
    if (gBle == nil) {
        return;
    }
    dispatch_async(gBle.q, ^{
        if (on) {
            if (gBle.cm.state == CBManagerStatePoweredOn) {
                lwHarvestConnected();
                if (!gBle.cm.isScanning) {
                    lwMsg(@"scan start");
                    NSDictionary *opts = @{CBCentralManagerScanOptionAllowDuplicatesKey : @YES};
                    [gBle.cm scanForPeripheralsWithServices:nil options:opts];
                }
            }
        } else {
            [gBle.cm stopScan];
        }
    });
}

void lw_ble_harvest(void) {
    if (gBle == nil) {
        return;
    }
    dispatch_async(gBle.q, ^{
        lwHarvestConnected();
    });
}

void lw_ble_connect(const char *cuuid) {
    if (gBle == nil) {
        return;
    }
    NSString *uuid = lwKey([NSString stringWithUTF8String:cuuid]);
    dispatch_async(gBle.q, ^{
        CBPeripheral *p = gBle.prphs[uuid];
        if (p == nil) {
            NSUUID *nu = [[NSUUID alloc] initWithUUIDString:uuid];
            NSArray *known = nu == nil ? @[] : [gBle.cm retrievePeripheralsWithIdentifiers:@[ nu ]];
            if (known.count > 0) {
                p = known[0];
                gBle.prphs[uuid] = p;
                lwMsg([NSString stringWithFormat:@"retrieve hit %@", uuid]);
            }
        }
        if (p == nil) {
            NSArray *connected = [gBle.cm retrieveConnectedPeripheralsWithServices:@[ gSvc ]];
            for (CBPeripheral *cp in connected) {
                if ([lwKey(cp.identifier.UUIDString) isEqualToString:uuid]) {
                    p = cp;
                    gBle.prphs[uuid] = p;
                    lwMsg([NSString stringWithFormat:@"already connected %@", uuid]);
                    break;
                }
            }
        }
        if (p == nil) {
            // Do not signal gone: that used to busy-loop connect until timeout
            // and then drop the power packet. Scan, then wait for goBLEFound.
            lwMsg([NSString stringWithFormat:@"uuid not cached, scanning %@", uuid]);
            goBLENeedScan((char *)uuid.UTF8String);
            if (gBle.cm.state == CBManagerStatePoweredOn && !gBle.cm.isScanning) {
                [gBle.cm scanForPeripheralsWithServices:nil options:nil];
            }
            return;
        }
        if (p.state == CBPeripheralStateConnected && gBle.chars[uuid] != nil) {
            goBLEReady((char *)uuid.UTF8String);
            return;
        }
        if (p.state == CBPeripheralStateConnected) {
            p.delegate = gBle;
            lwMsg([NSString stringWithFormat:@"connected, rediscovering %@", uuid]);
            [p discoverServices:@[ gSvc ]];
            return;
        }
        lwMsg([NSString stringWithFormat:@"connecting %@ state=%ld", uuid, (long)p.state]);
        [gBle.cm connectPeripheral:p options:nil];
    });
}

void lw_ble_cancel(const char *cuuid) {
    if (gBle == nil) {
        return;
    }
    NSString *uuid = lwKey([NSString stringWithUTF8String:cuuid]);
    dispatch_async(gBle.q, ^{
        CBPeripheral *p = gBle.prphs[uuid];
        if (p != nil) {
            [gBle.cm cancelPeripheralConnection:p];
        }
    });
}

void lw_ble_write(const char *cuuid, const void *buf, int len) {
    if (gBle == nil) {
        return;
    }
    NSString *uuid = lwKey([NSString stringWithUTF8String:cuuid]);
    NSData *data = [NSData dataWithBytes:buf length:(NSUInteger)len];
    dispatch_async(gBle.q, ^{
        CBPeripheral *p = gBle.prphs[uuid];
        CBCharacteristic *c = gBle.chars[uuid];
        if (p == nil || c == nil) {
            lwMsg([NSString stringWithFormat:@"write missing peripheral/char %@", uuid]);
            goBLEWriteFail((char *)uuid.UTF8String);
            return;
        }
        if (p.state != CBPeripheralStateConnected) {
            lwMsg([NSString stringWithFormat:@"write while not connected %@ state=%ld", uuid, (long)p.state]);
            goBLEWriteFail((char *)uuid.UTF8String);
            return;
        }
        const unsigned char *b = data.bytes;
        BOOL isKeepAlive = data.length >= 2 && b[0] == 0xAA;
        BOOL isPower = data.length >= 3 && b[0] == 0x33 && b[1] == 0x01;
        CBCharacteristicWriteType type = CBCharacteristicWriteWithoutResponse;
        if (isPower && (c.properties & CBCharacteristicPropertyWrite)) {
            type = CBCharacteristicWriteWithResponse;
        } else if ((c.properties & CBCharacteristicPropertyWriteWithoutResponse) == 0) {
            if (c.properties & CBCharacteristicPropertyWrite) {
                type = CBCharacteristicWriteWithResponse;
            } else {
                lwMsg([NSString stringWithFormat:@"no write property %@ 0x%lx", uuid,
                                                 (unsigned long)c.properties]);
                goBLEWriteFail((char *)uuid.UTF8String);
                return;
            }
        }
        if (!isKeepAlive) {
            lwMsg([NSString stringWithFormat:@"write %@ type=%@ pkt=%@", uuid,
                                             type == CBCharacteristicWriteWithResponse ? @"rsp" : @"no-rsp",
                                             lwHex(data)]);
        }
        [p writeValue:data forCharacteristic:c type:type];
    });
}
