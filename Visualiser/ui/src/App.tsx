import { useEffect, useRef, useState } from 'react';
import * as PIXI from 'pixi.js';
import './index.css';

interface Robot {
    id: string;
    x: number;
    y: number;
    heading: number;
    is_leader?: boolean;
    communication_range?: number;
    in_range_peer_ids?: string[];
    raft_term?: number;
    raft_log_index?: number;
    commit_index?: number;
    verified_casualty_ids?: string[];
}

interface Obstacle {
    id: string;
    x: number;
    y: number;
    width: number;
    height: number;
}

interface Environment {
    width: number;
    height: number;
    obstacles?: Obstacle[];
    is_paused?: boolean;
}

interface ControlAck {
    type?: string;
    ok?: boolean;
    is_paused?: boolean;
    error?: string;
    killed_robot_id?: string;
    applied_count?: number;
}

type PartitionGroup = 1 | 2 | 3;
type PartitionDraft = Record<string, PartitionGroup>;

interface LeaderLogEntry {
    current_leader: string;
    term: number;
    index: number;
    message: string;
    status: number;
    timestamp_unix_ms: number;
}

interface LeaderLogData {
    current_leader?: string;
    current_term?: number;
    entries?: LeaderLogEntry[];
}

interface StatePayload {
    environment?: Environment;
    robots?: Robot[];
    leader_log?: LeaderLogData;
    control?: ControlAck;
}

const LANDMARK_PREFIX = 'landmark:';
const SENSOR_RANGE = 100;
const CASUALTY_COLOR_UNVERIFIED = 0x00e5ff;
const CASUALTY_COLOR_VERIFIED = 0xffd84d;
const CASUALTY_COLOR_COMMITTED = 0x5dff72;

const hslToHexColor = (h: number, s: number, l: number): number => {
    const sat = s / 100;
    const light = l / 100;
    const chroma = (1 - Math.abs(2 * light - 1)) * sat;
    const huePrime = h / 60;
    const x = chroma * (1 - Math.abs((huePrime % 2) - 1));

    let r = 0;
    let g = 0;
    let b = 0;

    if (huePrime >= 0 && huePrime < 1) {
        r = chroma;
        g = x;
    } else if (huePrime >= 1 && huePrime < 2) {
        r = x;
        g = chroma;
    } else if (huePrime >= 2 && huePrime < 3) {
        g = chroma;
        b = x;
    } else if (huePrime >= 3 && huePrime < 4) {
        g = x;
        b = chroma;
    } else if (huePrime >= 4 && huePrime < 5) {
        r = x;
        b = chroma;
    } else {
        r = chroma;
        b = x;
    }

    const match = light - chroma / 2;
    const toByte = (value: number) => Math.round((value + match) * 255);
    const red = toByte(r);
    const green = toByte(g);
    const blue = toByte(b);
    return (red << 16) | (green << 8) | blue;
};

// Deterministic color generation based on Robot ID using a hash function.
// Ensure that a specific Robot ID always gets the same color
// Different IDs get colors that are visually spread apart
const colorForRobot = (robotID: string): number => {
    let hash = 2166136261;
    for (let i = 0; i < robotID.length; i++) {
        hash ^= robotID.charCodeAt(i);
        hash = Math.imul(hash, 16777619);
    }

    const hue = (hash >>> 0) % 360;
    return hslToHexColor(hue, 85, 56);
};

const drawDottedEllipse = (
    graphics: PIXI.Graphics,
    cx: number,
    cy: number,
    radiusX: number,
    radiusY: number,
    color: number,
    alpha: number
) => {
    const circumference = Math.PI * (3 * (radiusX + radiusY) - Math.sqrt((3 * radiusX + radiusY) * (radiusX + 3 * radiusY)));
    const dotCount = Math.max(24, Math.ceil(circumference / 14));
    const dotRadius = Math.max(0.6, Math.min(radiusX, radiusY) * 0.012);

    // graphics.fill({ color, alpha });
    for (let i = 0; i < dotCount; i++) {
        const angle = (i / dotCount) * Math.PI * 2;
        graphics.circle(cx + Math.cos(angle) * radiusX, cy + Math.sin(angle) * radiusY, dotRadius);
    }
    graphics.fill({color, alpha});
};

const isLeaderPingEntry = (entry: LeaderLogEntry): boolean => entry.message.startsWith('leader-ping:');

const isCasualtyVerificationEntry = (entry: LeaderLogEntry): boolean => {
    return entry.message.length > 0 && !isLeaderPingEntry(entry);
};

const casualtyIdFromLandmarkId = (landmarkId: string): string | null => {
    if (!landmarkId.startsWith(LANDMARK_PREFIX)) {
        return null;
    }

    const remainder = landmarkId.slice(LANDMARK_PREFIX.length);
    const separatorIndex = remainder.lastIndexOf(':');
    if (separatorIndex <= 0) {
        return null;
    }

    return remainder.slice(0, separatorIndex);
};

const verifiedCasualtyIdsFromRobots = (robots: Robot[]): Set<string> => {
    const verifiedIds = new Set<string>();

    robots.forEach((robot) => {
        (robot.verified_casualty_ids ?? []).forEach((casualtyId) => {
            verifiedIds.add(casualtyId);
        });
    });

    return verifiedIds;
};

const casualtyIdIsCommitted = (casualtyId: string, leaderLogData: LeaderLogData): boolean => {
    return (leaderLogData.entries ?? []).some((entry) => {
        return isCasualtyVerificationEntry(entry) && entry.message === casualtyId && entry.status === 2;
    });
};

const hasSameIDs = (left: string[], right: string[]): boolean => {
    if (left.length !== right.length) return false;
    for (let i = 0; i < left.length; i++) {
        if (left[i] !== right[i]) return false;
    }
    return true;
};

function App() {
    // these need to be mutable without causing React re-renders, so useRef
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const wsRef = useRef<WebSocket | null>(null);
    const appRef = useRef<PIXI.Application | null>(null);
    const spritesRef = useRef<Map<string, PIXI.Graphics>>(new Map());
    const labelsRef = useRef<Map<string, PIXI.Text>>(new Map());
    const envGraphicsRef = useRef<PIXI.Graphics | null>(null);
    const commGraphicsRef = useRef<PIXI.Graphics | null>(null);

    const [env, setEnv] = useState<Environment>({ width: 1000, height: 1000, obstacles: [] });
    const [leaderLog, setLeaderLog] = useState<LeaderLogData>({ current_leader: '', current_term: 0, entries: [] });
    const [robots, setRobots] = useState<Robot[]>([]);
    const [hideLeaderPings, setHideLeaderPings] = useState(true);
    const [showNetworkRange, setShowNetworkRange] = useState(true);
    const [showSensorRange, setShowSensorRange] = useState(true);
    const [activeTool, setActiveTool] = useState<'leader-log' | 'robot-killer' | 'partioning'>('leader-log');
    const [connected, setConnected] = useState(false);
    const [isPaused, setIsPaused] = useState(false);
    const [pausePending, setPausePending] = useState(false);
    const [pauseError, setPauseError] = useState<string | null>(null);
    const [robotCount, setRobotCount] = useState(0);
    const [killPending, setKillPending] = useState(false);
    const [killError, setKillError] = useState<string | null>(null);
    const [lastKilledId, setLastKilledId] = useState<string | null>(null);
    const [killTargetId, setKillTargetId] = useState('');
    const [knownRobotIDs, setKnownRobotIDs] = useState<string[]>([]);
    const [partitionDraft, setPartitionDraft] = useState<PartitionDraft>({});
    const [partitionPending, setPartitionPending] = useState(false);
    const [partitionError, setPartitionError] = useState<string | null>(null);
    const [lastPartitionAppliedCount, setLastPartitionAppliedCount] = useState<number | null>(null);

    // Initialize PixiJS
    useEffect(() => {
        if (!canvasRef.current) return;

        const initPixi = async () => {
            const app = new PIXI.Application();
            await app.init({
                canvas: canvasRef.current!,
                width: 1000,
                height: 1000,
                backgroundColor: 0x111116,
                resolution: window.devicePixelRatio || 1,
                autoDensity: true,
            });

            const backgroundGraphics = new PIXI.Graphics();
            app.stage.addChild(backgroundGraphics);
            envGraphicsRef.current = backgroundGraphics;

            const communicationGraphics = new PIXI.Graphics();
            app.stage.addChild(communicationGraphics);
            commGraphicsRef.current = communicationGraphics;

            appRef.current = app;
        };

        initPixi();

        return () => {
            if (appRef.current) {
                appRef.current.destroy(true, { children: true });
                appRef.current = null;
            }
        };
    }, []);

    // WebSocket Connection
    useEffect(() => {
        // In production/Docker, window.location.host works.
        // For local UI dev via Vite, fallback to localhost:8080.
        const wsUrl = import.meta.env.DEV ? 'ws://localhost:8080/ws' : `ws://${window.location.host}/ws`;
        const ws = new WebSocket(wsUrl);
        wsRef.current = ws;

        ws.onopen = () => {
            setConnected(true);
            setPauseError(null);
            console.log('Connected to Visualiser Server');
        };

        ws.onclose = () => {
            setConnected(false);
            setPausePending(false);
            setKillPending(false);
            setPartitionPending(false);
            console.log('Disconnected from server');
        };

        ws.onmessage = (event) => {
            try {
                const data: StatePayload = JSON.parse(event.data);

                if (data.control?.type === 'set_pause') {
                    setPausePending(false);
                    if (data.control.ok) {
                        setIsPaused(Boolean(data.control.is_paused));
                        setPauseError(null);
                    } else {
                        setPauseError(data.control.error ?? 'Failed to update pause state');
                    }
                }

                if (data.control?.type === 'kill') {
                    setKillPending(false);
                    if (data.control.ok) {
                        setKillError(null);
                        setLastKilledId(data.control.killed_robot_id ?? null);
                    } else {
                        setKillError(data.control.error ?? 'Failed to kill robot');
                        setLastKilledId(null);
                    }
                }

                if (data.control?.type === 'set_partition') {
                    setPartitionPending(false);
                    if (data.control.ok) {
                        setPartitionError(null);
                        setLastPartitionAppliedCount(data.control.applied_count ?? null);
                    } else {
                        setPartitionError(data.control.error ?? 'Failed to apply network partition');
                        setLastPartitionAppliedCount(null);
                    }
                }

                if (data.environment) {
                    setEnv(data.environment);
                    setIsPaused(Boolean(data.environment.is_paused));
                    if (appRef.current && envGraphicsRef.current) {
                        updateEnvironment(data.environment, envGraphicsRef.current);
                    }
                }
                if (data.robots && appRef.current) {
                    setRobots(data.robots);
                    setRobotCount(data.robots.length);
                    updateRobots(data.robots, appRef.current);

                    const sortedIDs = data.robots.map((robot) => robot.id).sort((left, right) => left.localeCompare(right));
                    setKnownRobotIDs((previous) => (hasSameIDs(previous, sortedIDs) ? previous : sortedIDs));
                }

                if (data.leader_log) {
                    setLeaderLog(data.leader_log);
                }
            } catch (err) {
                setPausePending(false);
                setKillPending(false);
                setPartitionPending(false);
                console.error('Error parsing WebSocket message', err);
            }
        };

        return () => {
            ws.close();
            wsRef.current = null;
        };
    }, []);

    useEffect(() => {
        setPartitionDraft((previous) => {
            const next: PartitionDraft = {};
            let changed = false;

            knownRobotIDs.forEach((robotID) => {
                const existing = previous[robotID] ?? 1;
                next[robotID] = existing;
                if (previous[robotID] !== existing) {
                    changed = true;
                }
            });

            if (!changed) {
                const previousKeys = Object.keys(previous);
                if (previousKeys.length !== knownRobotIDs.length) {
                    changed = true;
                }
            }

            return changed ? next : previous;
        });
    }, [knownRobotIDs]);

    useEffect(() => {
        if (appRef.current && envGraphicsRef.current) {
            updateEnvironment(env, envGraphicsRef.current);
        }
    }, [env, leaderLog, robots]);

    useEffect(() => {
        if (appRef.current && commGraphicsRef.current) {
            updateCommunicationOverlay(robots);
        }
    }, [robots, env.width, env.height, showNetworkRange, showSensorRange]);

    const togglePause = () => {
        const ws = wsRef.current;
        if (!ws || ws.readyState !== WebSocket.OPEN || pausePending) return;

        const nextPaused = !isPaused;
        setPausePending(true);
        setPauseError(null);

        try {
            ws.send(JSON.stringify({ type: 'set_pause', pause: nextPaused }));
        } catch (error) {
            setPausePending(false);
            setPauseError('Failed to send pause command');
            console.error('Error sending pause command', error);
        }
    };

    const killRobot = () => {
        const ws = wsRef.current;
        if (!ws || ws.readyState !== WebSocket.OPEN || killPending || robotCount === 0) return;

        setKillPending(true);
        setKillError(null);
        setLastKilledId(null);

        try {
            ws.send(JSON.stringify({ type: 'kill' }));
        } catch (error) {
            setKillPending(false);
            setKillError('Failed to send kill command');
            console.error('Error sending kill command', error);
        }
    };

    const killRobotById = () => {
        const ws = wsRef.current;
        const robotId = killTargetId.trim();

        if (!ws || ws.readyState !== WebSocket.OPEN || killPending || robotId.length !== 5) return;

        setKillPending(true);
        setKillError(null);
        setLastKilledId(null);

        try {
            ws.send(JSON.stringify({ type: 'kill', robot_id: robotId }));
        } catch (error) {
            setKillPending(false);
            setKillError('Failed to send kill command');
            console.error('Error sending kill command', error);
        }
    };

    const updateRobotPartitionGroup = (robotID: string, nextGroup: PartitionGroup) => {
        setPartitionDraft((previous) => {
            if (previous[robotID] === nextGroup) {
                return previous;
            }
            return {
                ...previous,
                [robotID]: nextGroup,
            };
        });
        setPartitionError(null);
        setLastPartitionAppliedCount(null);
    };

    const applyPartitioning = () => {
        const ws = wsRef.current;
        if (!ws || ws.readyState !== WebSocket.OPEN || partitionPending || knownRobotIDs.length === 0) return;

        const assignments = knownRobotIDs.map((robotID) => ({
            robot_id: robotID,
            group_index: partitionDraft[robotID] ?? 1,
        }));

        setPartitionPending(true);
        setPartitionError(null);
        setLastPartitionAppliedCount(null);

        try {
            ws.send(JSON.stringify({ type: 'set_partition', assignments }));
        } catch (error) {
            setPartitionPending(false);
            setPartitionError('Failed to send partition command');
            console.error('Error sending partition command', error);
        }
    };

    const updateEnvironment = (environment: Environment, graphics: PIXI.Graphics) => {
        graphics.clear();

        const scaleX = 1000 / environment.width;
        const scaleY = 1000 / environment.height;

        // Subtle background grid boundary
        graphics.rect(0, 0, 1000, 1000);
        graphics.stroke({ width: 2, color: 0x444455 });

        if (environment.obstacles) {
            const staticObstacles = environment.obstacles.filter((obs) => !obs.id.startsWith(LANDMARK_PREFIX));
            const landmarks = environment.obstacles.filter((obs) => obs.id.startsWith(LANDMARK_PREFIX));
            const verifiedCasualtyIds = verifiedCasualtyIdsFromRobots(robots);

            graphics.fill(0xff3366, 0.5); // Walls/obstacles
            staticObstacles.forEach((obs) => {
                graphics.rect(
                    obs.x * scaleX,
                    obs.y * scaleY,
                    obs.width * scaleX,
                    obs.height * scaleY
                );
            });
            graphics.fill();

            // Render landmarks as bright circles so they stand out from walls.
            landmarks.forEach((lm) => {
                const cx = (lm.x + lm.width / 2) * scaleX;
                const cy = (lm.y + lm.height / 2) * scaleY;
                const radius = Math.max(8, (lm.width * scaleX) / 2);

                let color = 0xffcc00; // default landmark color
                if (lm.id.includes(':casualty')) {
                    const casualtyId = casualtyIdFromLandmarkId(lm.id);
                    if (casualtyId && casualtyIdIsCommitted(casualtyId, leaderLog)) {
                        color = CASUALTY_COLOR_COMMITTED;
                    } else if (casualtyId && verifiedCasualtyIds.has(casualtyId)) {
                        color = CASUALTY_COLOR_VERIFIED;
                    } else {
                        color = CASUALTY_COLOR_UNVERIFIED;
                    }
                }
                if (lm.id.includes(':corridor')) color = 0x7dff6d;
                if (lm.id.includes(':obstacle')) color = 0xffa347;

                graphics.circle(cx, cy, radius);
                graphics.fill({ color, alpha: 0.95 });
                graphics.stroke({ width: 1.5, color: 0x111116, alpha: 0.8 });
            });
        }
    };

    const statusLabel = (status: number) => {
        if (status === 2) return 'Confirmed';
        if (status === 1) return 'Pending confirmation';
        return 'Unknown';
    };

    const statusClassName = (status: number) => {
        if (status === 2) return 'confirmed';
        if (status === 1) return 'pending';
        return 'unknown';
    };

    const sortedLeaderEntries = (leaderLog.entries ?? []).slice().sort((a, b) => b.index - a.index);
    const visibleLeaderEntries = hideLeaderPings
        ? sortedLeaderEntries.filter((entry) => !isLeaderPingEntry(entry))
        : sortedLeaderEntries;

    const isLeaderLogTabActive = activeTool === 'leader-log';
    const isRobotKillerTabActive = activeTool === 'robot-killer';
    const isPartioningTabActive = activeTool === 'partioning';
    const groupOneCount = knownRobotIDs.filter((robotID) => (partitionDraft[robotID] ?? 1) === 1).length;
    const groupTwoCount = knownRobotIDs.filter((robotID) => (partitionDraft[robotID] ?? 1) === 2).length;
    const groupThreeCount = knownRobotIDs.filter((robotID) => (partitionDraft[robotID] ?? 1) === 3).length;

    const updateCommunicationOverlay = (robots: Robot[]) => {
        const graphics = commGraphicsRef.current;
        if (!graphics) return;

        graphics.clear();

        if (env.width <= 0 || env.height <= 0) return;

        const scaleX = 1000 / env.width;
        const scaleY = 1000 / env.height;
        const robotsById = new Map<string, Robot>(robots.map((robot) => [robot.id, robot]));

        const drawnPairs = new Set<string>();
        robots.forEach((robot) => {
            const peerIds = robot.in_range_peer_ids ?? [];
            peerIds.forEach((peerId) => {
                const peer = robotsById.get(peerId);
                if (!peer) return;

                const lowID = robot.id < peerId ? robot.id : peerId;
                const highID = robot.id < peerId ? peerId : robot.id;
                const pairKey = `${lowID}|${highID}`;
                if (drawnPairs.has(pairKey)) return;

                drawnPairs.add(pairKey);
                const startX = robot.x * scaleX;
                const startY = robot.y * scaleY;
                const endX = peer.x * scaleX;
                const endY = peer.y * scaleY;
                const midX = (startX + endX) / 2;
                const midY = (startY + endY) / 2;

                graphics.moveTo(startX, startY);
                graphics.lineTo(midX, midY);
                graphics.stroke({ width: 2.4, color: colorForRobot(robot.id), alpha: 0.95 });

                graphics.moveTo(midX, midY);
                graphics.lineTo(endX, endY);
                graphics.stroke({ width: 2.4, color: colorForRobot(peer.id), alpha: 0.95 });
            });
        });

        robots.forEach((robot) => {
            const range = robot.communication_range ?? 0;
            if (range <= 0) return;

            const radiusX = range * scaleX;
            const radiusY = range * scaleY;

            // graphics.ellipse(robot.x * scaleX, robot.y * scaleY, radiusX, radiusY);
            // graphics.stroke({ width: 2.2, color: colorForRobot(robot.id), alpha: 0.78 });

            const centerX = robot.x * scaleX;
            const centerY = robot.y * scaleY;

            if (showSensorRange) {
                const sensorRadiusX = SENSOR_RANGE * scaleX;
                const sensorRadiusY = SENSOR_RANGE * scaleY;
                graphics.ellipse(centerX, centerY, sensorRadiusX, sensorRadiusY);
                graphics.stroke({ width: 2, color: colorForRobot(robot.id), alpha: 0.9 });
            }

            if (showNetworkRange) {
                drawDottedEllipse(graphics, centerX, centerY, radiusX, radiusY, colorForRobot(robot.id), 0.78);
            }


        });
    };

    // Update logic
    const updateRobots = (robots: Robot[], app: PIXI.Application) => {
        const sprites = spritesRef.current;
        const labels = labelsRef.current;

        // Scale coords to canvas (assuming environment 1000x1000 -> 1000x1000 canvas)
        const scaleX = 1000 / env.width;
        const scaleY = 1000 / env.height;

        // Track seen IDs to remove dead robots
        const seenIds = new Set<string>();

        robots.forEach((robot) => {
            seenIds.add(robot.id);

            let sprite = sprites.get(robot.id);
            if (!sprite) {
                // Create new robot sprite
                sprite = new PIXI.Graphics();
                app.stage.addChild(sprite);
                sprites.set(robot.id, sprite);
            }

            let label = labels.get(robot.id);
            if (!label) {
                label = new PIXI.Text({
                    text: '',
                    style: {
                        fontFamily: 'Courier New',
                        fontSize: 14,
                        fontWeight: '700',
                        fill: 0xffffff,
                        stroke: { color: 0x101014, width: 3 },
                    },
                });
                label.anchor.set(0, 0.5);
                app.stage.addChild(label);
                labels.set(robot.id, label);
            }

            sprite.clear();
            if (robot.is_leader) {
                sprite.rect(-9, -9, 18, 18);
            } else {
                sprite.poly([
                    0, -10,
                    -8, 10,
                    8, 10
                ]);
            }
            sprite.fill(colorForRobot(robot.id));

            // Update position and rotation
            sprite.x = robot.x * scaleX;
            sprite.y = robot.y * scaleY;
            sprite.rotation = robot.is_leader ? 0 : robot.heading;

            const idPrefix = robot.id.slice(0, 5);
            const term = robot.raft_term ?? 0;
            const logIndex = robot.raft_log_index ?? -1;
            const commitIndex = robot.commit_index ?? -1;
            label.text = `${idPrefix} T:${term} I:${logIndex} C:${commitIndex}`;
            label.x = sprite.x + 11;
            label.y = sprite.y + 11;
        });

        // Clean up vanished robots
        sprites.forEach((sprite, id) => {
            if (!seenIds.has(id)) {
                app.stage.removeChild(sprite);
                sprite.destroy();
                sprites.delete(id);

                const label = labels.get(id);
                if (label) {
                    app.stage.removeChild(label);
                    label.destroy();
                    labels.delete(id);
                }
            }
        });
    };

    return (
        <div className="dashboard-container">
            <header className="header glass">
                <h1>Swarm Visualiser</h1>
                <div className="header-controls">
                    <button
                        type="button"
                        className={`pause-button ${isPaused ? 'resume' : 'pause'}`}
                        onClick={togglePause}
                        disabled={!connected || pausePending}
                    >
                        {pausePending ? 'Applying...' : isPaused ? 'Resume' : 'Pause'}
                    </button>
                    <div className="range-toggle-group" role="group" aria-label="Range visibility toggles">
                        <button
                            type="button"
                            className={`range-toggle-button ${showNetworkRange ? 'active' : ''}`}
                            onClick={() => setShowNetworkRange((previous) => !previous)}
                            aria-pressed={showNetworkRange}
                        >
                            Network range
                        </button>
                        <button
                            type="button"
                            className={`range-toggle-button ${showSensorRange ? 'active' : ''}`}
                            onClick={() => setShowSensorRange((previous) => !previous)}
                            aria-pressed={showSensorRange}
                        >
                            Sensor range
                        </button>
                    </div>
                </div>
                <div className="status-indicators">
                    <div className={`status-dot ${connected ? 'connected' : 'disconnected'}`}></div>
                    <span>{connected ? 'Live' : 'Offline'}</span>
                </div>
            </header>

            <main className="main-content">
                <aside className="sidebar glass">
                    <div className="sidebar-tabs" role="tablist" aria-label="Visualiser tools">
                        <button
                            type="button"
                            className={`sidebar-tab ${isLeaderLogTabActive ? 'active' : ''}`}
                            onClick={() => setActiveTool('leader-log')}
                            role="tab"
                            aria-selected={isLeaderLogTabActive}
                        >
                            Leader Log
                        </button>
                        <button
                            type="button"
                            className={`sidebar-tab ${isRobotKillerTabActive ? 'active' : ''}`}
                            onClick={() => setActiveTool('robot-killer')}
                            role="tab"
                            aria-selected={isRobotKillerTabActive}
                        >
                            Kill
                        </button>
                        <button
                            type="button"
                            className={`sidebar-tab ${isPartioningTabActive ? 'active' : ''}`}
                            onClick={() => setActiveTool('partioning')}
                            role="tab"
                            aria-selected={isPartioningTabActive}
                        >
                            Partitioning
                        </button>
                    </div>

                    <div className="sidebar-panel" role="tabpanel">
                        {isLeaderLogTabActive && (
                            <section className="tool-panel tool-panel-scrollable">
                                <label className="leader-log-filter">
                                    <input
                                        type="checkbox"
                                        checked={hideLeaderPings}
                                        onChange={(event) => setHideLeaderPings(event.target.checked)}
                                    />
                                    <span>Hide leader ping entries</span>
                                </label>
                                <div className="leader-summary">
                                    <div className="summary-row">
                                        <span className="label">Current Leader</span>
                                        <span className="value">{leaderLog.current_leader || 'None'}</span>
                                    </div>
                                    <div className="summary-row">
                                        <span className="label">Current Term</span>
                                        <span className="value">{leaderLog.current_term ?? 0}</span>
                                    </div>
                                </div>

                                <div className="log-list" role="list" aria-label="Leader raft log entries">
                                    {visibleLeaderEntries.map((entry) => (
                                        <article className="log-entry" key={`${entry.term}-${entry.index}-${entry.timestamp_unix_ms}`} role="listitem">
                                            <div className="log-entry-top">
                                                <span className="log-meta">Leader {entry.current_leader || leaderLog.current_leader || 'unknown'}</span>
                                                <span className={`log-status ${statusClassName(entry.status)}`}>{statusLabel(entry.status)}</span>
                                            </div>
                                            <div className="log-grid">
                                                <span>Term: {entry.term}</span>
                                                <span>Index: {entry.index}</span>
                                            </div>
                                            <div className="log-message">{entry.message || '(empty message)'}</div>
                                        </article>
                                    ))}
                                    {visibleLeaderEntries.length === 0 && (
                                        <div className="log-empty">
                                            {hideLeaderPings && (leaderLog.entries ?? []).length > 0
                                                ? 'No leader log entries match the current filter.'
                                                : 'No leader log entries yet.'}
                                        </div>
                                    )}
                                </div>
                            </section>
                        )}

                        {isRobotKillerTabActive && (
                            <section className="tool-panel">
                                <div className="kill-robot-section">
                                    <div className="kill-action-row">
                                        <button
                                            type="button"
                                            className="kill-random-button"
                                            onClick={killRobot}
                                            disabled={!connected || killPending || robotCount === 0}
                                        >
                                            {killPending ? 'Killing…' : 'Kill Random'}
                                        </button>
                                    </div>
                                    <div className="kill-action-row">
                                        <input
                                            type="text"
                                            className="kill-id-input"
                                            value={killTargetId}
                                            onChange={(event) => setKillTargetId(event.target.value.slice(0, 5))}
                                            placeholder="Robot ID"
                                            maxLength={5}
                                            inputMode="text"
                                            autoComplete="off"
                                            aria-label="Robot ID prefix to kill"
                                        />
                                        <button
                                            type="button"
                                            className="kill-target-button"
                                            onClick={killRobotById}
                                            disabled={!connected || killPending || killTargetId.trim().length !== 5}
                                        >
                                            Kill
                                        </button>
                                    </div>
                                    {lastKilledId && (
                                        <div className="kill-robot-feedback" role="status">
                                            Removed <span className="mono">{lastKilledId}</span> from simulation
                                        </div>
                                    )}
                                    {killError && <div className="control-error">{killError}</div>}
                                </div>
                            </section>
                        )}

                        {isPartioningTabActive && (
                            <section className="tool-panel tool-panel-scrollable">
                                <div className="partition-section">
                                    <div className="partition-summary" aria-label="Partition group totals">
                                        <span>G1: {groupOneCount}</span>
                                        <span>G2: {groupTwoCount}</span>
                                        <span>G3: {groupThreeCount}</span>
                                    </div>

                                    <div className="partition-list" role="list" aria-label="Robot partition assignments">
                                        {knownRobotIDs.map((robotID) => (
                                            <div className="partition-row" role="listitem" key={robotID}>
                                                <span className="partition-robot-id mono">{robotID.slice(0, 5)}</span>
                                                <label className="partition-select-label" htmlFor={`partition-${robotID}`}>
                                                    Group
                                                </label>
                                                <select
                                                    id={`partition-${robotID}`}
                                                    className="partition-select"
                                                    value={partitionDraft[robotID] ?? 1}
                                                    onChange={(event) => updateRobotPartitionGroup(robotID, Number(event.target.value) as PartitionGroup)}
                                                    disabled={partitionPending}
                                                >
                                                    <option value={1}>1</option>
                                                    <option value={2}>2</option>
                                                    <option value={3}>3</option>
                                                </select>
                                            </div>
                                        ))}
                                        {knownRobotIDs.length === 0 && (
                                            <div className="log-empty">No robots available.</div>
                                        )}
                                    </div>

                                    <div className="partition-footer">
                                        <button
                                            type="button"
                                            className="kill-target-button"
                                            onClick={applyPartitioning}
                                            disabled={!connected || partitionPending || knownRobotIDs.length === 0}
                                        >
                                            {partitionPending ? 'Applying…' : 'Implement'}
                                        </button>
                                        {lastPartitionAppliedCount !== null && (
                                            <div className="kill-robot-feedback" role="status">
                                                Applied grouping for {lastPartitionAppliedCount} robots
                                            </div>
                                        )}
                                        {partitionError && <div className="control-error">{partitionError}</div>}
                                    </div>
                                </div>
                            </section>
                        )}
                    </div>

                    {pauseError && <div className="control-error">{pauseError}</div>}
                </aside>

                <section className="simulation-view glass">
                    <div className="canvas-wrapper">
                        <canvas ref={canvasRef} />
                    </div>
                </section>
            </main>
        </div>
    );
}

export default App;
