// CoreBluetooth bridge for Govee BLE lamps.
//
// Everything runs on a private serial dispatch queue — the CBCentralManager
// delegate queue — and never touches the main thread. (The off-the-shelf Go
// BLE bindings pump a nested run loop on the main queue, which corrupts the
// AppKit loop Wails owns and crashes the app.)

#import <Foundation/Foundation.h>
#import <CoreBluetooth/CoreBluetooth.h>
#include "_cgo_export.h"

@interface LWBle : NSObject <CBCentralManagerDelegate, CBPeripheralDelegate>
@property(strong) CBCentralManager *cm;
@property(strong) dispatch_queue_t q;
@property(strong) NSMutableDictionary<NSString *, CBPeripheral *> *prphs;
@property(strong) NSMutableDictionary<NSString *, CBCharacteristic *> *chars;
@end

static LWBle *gBle = nil;
static CBUUID *gSvc = nil;
static CBUUID *gChr = nil;

@implementation LWBle

- (instancetype)init {
    self = [super init];
    if (self) {
        _q = dispatch_queue_create("lightwave.ble", DISPATCH_QUEUE_SERIAL);
        _prphs = [NSMutableDictionary new];
        _chars = [NSMutableDictionary new];
        _cm = [[CBCentralManager alloc] initWithDelegate:self queue:_q];
    }
    return self;
}

- (void)centralManagerDidUpdateState:(CBCentralManager *)central {
    goBLEState(central.state == CBManagerStatePoweredOn ? 1 : 0);
}

- (void)centralManager:(CBCentralManager *)central
    didDiscoverPeripheral:(CBPeripheral *)p
        advertisementData:(NSDictionary *)ad
                     RSSI:(NSNumber *)rssi {
    NSString *name = ad[CBAdvertisementDataLocalNameKey] ?: p.name;
    if (name == nil || name.length == 0) {
        return;
    }
    NSString *uuid = p.identifier.UUIDString;
    self.prphs[uuid] = p;
    goBLEFound((char *)uuid.UTF8String, (char *)name.UTF8String);
}

- (void)centralManager:(CBCentralManager *)central didConnectPeripheral:(CBPeripheral *)p {
    p.delegate = self;
    [p discoverServices:@[ gSvc ]];
}

- (void)centralManager:(CBCentralManager *)central
    didFailToConnectPeripheral:(CBPeripheral *)p
                         error:(NSError *)e {
    goBLEGone((char *)p.identifier.UUIDString.UTF8String);
}

- (void)centralManager:(CBCentralManager *)central
    didDisconnectPeripheral:(CBPeripheral *)p
                      error:(NSError *)e {
    NSString *uuid = p.identifier.UUIDString;
    [self.chars removeObjectForKey:uuid];
    goBLEGone((char *)uuid.UTF8String);
}

- (void)peripheral:(CBPeripheral *)p didDiscoverServices:(NSError *)e {
    if (e == nil) {
        for (CBService *s in p.services) {
            if ([s.UUID isEqual:gSvc]) {
                [p discoverCharacteristics:@[ gChr ] forService:s];
                return;
            }
        }
    }
    [self.cm cancelPeripheralConnection:p];
}

- (void)peripheral:(CBPeripheral *)p
    didDiscoverCharacteristicsForService:(CBService *)s
                                   error:(NSError *)e {
    if (e == nil) {
        for (CBCharacteristic *c in s.characteristics) {
            if ([c.UUID isEqual:gChr]) {
                self.chars[p.identifier.UUIDString] = c;
                goBLEReady((char *)p.identifier.UUIDString.UTF8String);
                return;
            }
        }
    }
    [self.cm cancelPeripheralConnection:p];
}

@end

void lw_ble_init(void) {
    if (gBle != nil) {
        return;
    }
    gSvc = [CBUUID UUIDWithString:@"00010203-0405-0607-0809-0A0B0C0D1910"];
    gChr = [CBUUID UUIDWithString:@"00010203-0405-0607-0809-0A0B0C0D2B11"];
    gBle = [[LWBle alloc] init];
}

void lw_ble_scan(int on) {
    if (gBle == nil) {
        return;
    }
    dispatch_async(gBle.q, ^{
        if (on) {
            if (gBle.cm.state == CBManagerStatePoweredOn && !gBle.cm.isScanning) {
                [gBle.cm scanForPeripheralsWithServices:nil options:nil];
            }
        } else {
            [gBle.cm stopScan];
        }
    });
}

void lw_ble_connect(const char *cuuid) {
    if (gBle == nil) {
        return;
    }
    NSString *uuid = [NSString stringWithUTF8String:cuuid];
    dispatch_async(gBle.q, ^{
        CBPeripheral *p = gBle.prphs[uuid];
        if (p == nil) {
            // Not seen by a scan this run: ask CoreBluetooth for the known
            // peripheral so persisted lamps reconnect without a scan.
            NSUUID *nu = [[NSUUID alloc] initWithUUIDString:uuid];
            NSArray *known = nu == nil ? @[] : [gBle.cm retrievePeripheralsWithIdentifiers:@[ nu ]];
            if (known.count == 0) {
                goBLEGone((char *)uuid.UTF8String);
                return;
            }
            p = known[0];
            gBle.prphs[uuid] = p;
        }
        if (p.state == CBPeripheralStateConnected && gBle.chars[uuid] != nil) {
            goBLEReady((char *)uuid.UTF8String);
            return;
        }
        [gBle.cm connectPeripheral:p options:nil];
    });
}

void lw_ble_cancel(const char *cuuid) {
    if (gBle == nil) {
        return;
    }
    NSString *uuid = [NSString stringWithUTF8String:cuuid];
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
    NSString *uuid = [NSString stringWithUTF8String:cuuid];
    NSData *data = [NSData dataWithBytes:buf length:(NSUInteger)len];
    dispatch_async(gBle.q, ^{
        CBPeripheral *p = gBle.prphs[uuid];
        CBCharacteristic *c = gBle.chars[uuid];
        if (p == nil || c == nil) {
            return;
        }
        [p writeValue:data forCharacteristic:c type:CBCharacteristicWriteWithoutResponse];
    });
}
