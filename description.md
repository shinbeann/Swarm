This project builds a 2d simulation of a robot swarm performing a search and rescue mission in a disaster area. The robots are meant to communicate information with each other, map out the area and find survivors.  

The purpose is to implement distributed computing concepts between the robots, with the rest of the simulator being a way to visualize the swarm and its behaviour as a result of the distributed computing concepts. The intention is to make the rest of the simulator as simple as possible, so that the focus is on the distributed computing concepts.  

The project is split into the following major subprojects:  
* Communications: defines the gRPC service and messages used for communications between robots, world and the visualiser.
* Robot: defines the robot and its behaviour.  
* World: serves as the central authority for the simulation.  
* Visualiser: receives data from the world and visualizes them.  

# Architecture

The robots and the world engine are a hub and spoke model. The world serves as the source of truth for all physics, sensor and network data of the robots.  

As part of the control loop, robots will send a request for movement based on a position or director vector with velocity. The world will then update the robot's position and heading based on this request.  

The sensors of the robot are "faked", in the sense that we will not be using any emulation of real sensor behaviour. Instead, the world will provide the robots with the data that they would receive from their sensors. E.g. if the robot requests for sensor data, the world engine finds all the objects that are within the robot's sensor radius and returns them.  

The world engine also simulates the wireless communication network that will exist between the robots. This includes defining the network power, bandwidth and latency between robots, simulating real world conditions as robots move around. This is despite the robots actually having excellent network conditions in the simulation.  

Each robot launches as a docker container based on the same robot image. The world engine is a separate docker container, and the visualiser is a separate docker container, based on their respective images.  
All containers should be on the same docker network.  

A robot should only interact with another robot and the world engine. The visualiser should only interact with the world engine.  

# Subprojects Specifications

## Communications
This subproject should have the .proto files for the gRPC service and messages.  

Specifically, there should be the following services:
* RobotService: used for communication between world and robot, with the world as the server and the robots as the clients.  
    * MoveToPosition: robot signifies an intention to move to a position or along a direction vector with a velocity.  
    * GetSensorData: robot requests for sensor data from the world.  
    * GetNetworkData: robot requests for network data from the world.  
    * SendHeartbeat: robot sends a heartbeat to the world to indicate that it is still alive.  
* VisualiserService: used for communication between world and visualiser, with the world as the server and the visualiser as the client.  
    * GetEnvironmentData: visualiser requests for environment data from the world.  
    * GetRobotData: visualiser requests for robot data relevant to the visualiser from the world.  

Note that the grpc implementations of these messages and services should be put into the respective subprojects.  

## Robot

Each robot is an independent agent that runs its own control loop. It is responsible for its own movement, sensor data processing and decision making.  

The robot should be defined in Go. Each robot will run in its own docker container, with the whole swarm being deployed via docker compose.  

Robots will communicate with each other using gRPC, though networking conditions received from the world would artificially influence communication latency, bandwidth and reliability.  

Robots will also communicate with the world engine using gRPC.  

## World
The world subproject serves as the central authority for the simulation. It should be written in Go.  

For the purpose of this simulation, the world is a 2d plane with a defined boundary.  

## Visualiser

The visualiser subproject receives data from the world and visualizes them.  

It should be written to be viewable in a web browser. Choose simple representations for the robots, environment and other objects.  